// Bounded, same-origin client for the /v1 control-plane API.
//
// A transport helper, deliberately not a data framework: no cache, no store, no
// automatic retry. Retry safety is per-operation here exactly as it is in the
// CLI — a GET is safe to repeat, a lifecycle POST is not — so a generic retry
// layer would need knowledge this module does not have.
//
// Every bound below mirrors a number the server or the CLI already established.
// They are not new policy; they are the same policy, restated on the one client
// that cannot inherit it from Go.

// REQUEST_MAX_BYTES mirrors the server's maxAPIRequestBody. Checked before
// sending so the client never knowingly builds a request the server must
// reject, and measured on the encoded bytes because envelope overhead counts.
export const REQUEST_MAX_BYTES = 256 * 1024;

// RESPONSE_MAX_BYTES mirrors the CLI's maxPlatformResponseBody. Sized for the
// largest real response: a comparison carrying up to 1024 bounded behavioral
// deltas plus bounded text.
//
// A client that reads an unbounded body has made its own memory safety a
// property of the server behaving well. That is not worth assuming even on
// loopback, and it is why readBounded below streams rather than calling
// response.text().
export const RESPONSE_MAX_BYTES = 4 * 1024 * 1024;

// REQUEST_TIMEOUT_MS bounds one request. These are one-shot operations, and a
// page that hangs forever on an unreachable control plane is indistinguishable
// from a broken one. Not used for SSE, which is long-lived by design and has
// its own handshake and liveness bounds in realtime.js.
export const REQUEST_TIMEOUT_MS = 30000;

// ApiError separates a server refusal from a transport failure.
//
// The distinction is the browser's version of the CLI's exit codes: an
// operational failure must never be presentable as a gate result. Gate FAIL is
// only a successful compare response whose gate.verdict is "fail".
export class ApiError extends Error {
  constructor(message, { status = 0, code = "", operational = false } = {}) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    // operational: the request did not produce a server answer at all.
    this.operational = operational;
  }
}

// isCanonicalUint64 validates a uint64 as text.
//
// The API encodes uint64 counters as decimal strings because JSON numbers
// cannot represent them, and this is the only place the browser is more
// exposed than the CLI: JavaScript has no integer type that holds these
// values. So they are validated as text and never parsed — 0 is valid, and
// 18446744073709551615 must survive intact rather than rounding to
// 18446744073709552000.
const UINT64_MAX = "18446744073709551615";
export function isCanonicalUint64(text) {
  if (typeof text !== "string" || !/^(0|[1-9][0-9]*)$/.test(text)) {
    return false;
  }
  if (text.length > UINT64_MAX.length) {
    return false;
  }
  if (text.length === UINT64_MAX.length && text > UINT64_MAX) {
    return false;
  }
  return true;
}

// readBounded reads a response body under RESPONSE_MAX_BYTES.
//
// Streamed and cancelled on overflow rather than buffered and measured
// afterwards, because measuring afterwards means the memory was already spent.
async function readBounded(response) {
  if (response.body === null) {
    return "";
  }
  const reader = response.body.getReader();
  const chunks = [];
  let total = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) {
        break;
      }
      total += value.byteLength;
      if (total > RESPONSE_MAX_BYTES) {
        // Stop the transfer rather than draining it: the point of the bound is
        // not to read the rest.
        await reader.cancel();
        throw new ApiError(
          `response exceeded the ${RESPONSE_MAX_BYTES} byte limit`,
          { status: response.status, operational: true },
        );
      }
      chunks.push(value);
    }
  } finally {
    try {
      reader.releaseLock();
    } catch (ignored) {
      // Already released by cancel(); nothing to recover.
    }
  }

  const joined = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    joined.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return new TextDecoder("utf-8", { fatal: false }).decode(joined);
}

// errorFrom turns a non-2xx response into an ApiError.
//
// The server's envelope is { version, error: { code, message } }. A response
// that does not carry one is reported by status alone rather than guessed at —
// inventing a second error schema is how two clients end up disagreeing about
// what went wrong.
function errorFrom(response, text) {
  let code = "";
  let message = "";
  try {
    const parsed = JSON.parse(text);
    if (parsed && parsed.error && typeof parsed.error === "object") {
      if (typeof parsed.error.code === "string") {
        code = parsed.error.code;
      }
      if (typeof parsed.error.message === "string") {
        message = parsed.error.message;
      }
    }
  } catch (ignored) {
    // Not an envelope. Status is still meaningful.
  }
  if (message === "") {
    message = `the control plane returned HTTP ${response.status}`;
  }
  return new ApiError(message, { status: response.status, code });
}

// request issues one bounded, same-origin call.
//
// Relative URLs only: the UI is served from the same origin as the API, so
// there is no endpoint to configure and nothing that could point this at
// another host.
async function request(method, path, body) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);

  const init = {
    method,
    signal: controller.signal,
    headers: { Accept: "application/json" },
    // Same-origin by construction; stated so a redirect to another origin
    // cannot quietly carry the request there.
    credentials: "omit",
    mode: "same-origin",
    redirect: "error",
    cache: "no-store",
  };

  if (body !== undefined) {
    const encoded = JSON.stringify(body);
    const size = new TextEncoder().encode(encoded).byteLength;
    if (size > REQUEST_MAX_BYTES) {
      clearTimeout(timer);
      throw new ApiError(
        `request body is ${size} bytes, over the ${REQUEST_MAX_BYTES} byte limit`,
        { operational: true },
      );
    }
    init.headers["Content-Type"] = "application/json";
    init.body = encoded;
  }

  let response;
  try {
    response = await fetch(path, init);
  } catch (cause) {
    const reason = cause && cause.name === "AbortError"
      ? `no response within ${REQUEST_TIMEOUT_MS}ms`
      : "the control plane could not be reached";
    throw new ApiError(reason, { operational: true });
  } finally {
    clearTimeout(timer);
  }

  const text = await readBounded(response);

  if (!response.ok) {
    throw errorFrom(response, text);
  }
  if (text === "") {
    return {};
  }
  try {
    return JSON.parse(text);
  } catch (ignored) {
    // A 2xx that is not JSON is the environment misbehaving, not a refusal.
    throw new ApiError("the control plane returned a malformed response", {
      status: response.status,
      operational: true,
    });
  }
}

// segment escapes one caller-owned path segment.
//
// An identifier containing "/" must address one resource with that name, never
// two segments — the same rule the CLI's apiPath enforces.
function segment(value) {
  return encodeURIComponent(value);
}

// ---------------------------------------------------------------------
// Routes. Every one of these exists already; this task adds no API surface.
// ---------------------------------------------------------------------

export const createProject = (id, name) =>
  request("POST", "/v1/projects", { id, name });

export const getProject = (id) =>
  request("GET", `/v1/projects/${segment(id)}`);

export const createAgent = (id, projectID, name) =>
  request("POST", "/v1/agents", { id, project_id: projectID, name });

export const getAgent = (id) =>
  request("GET", `/v1/agents/${segment(id)}`);

export const createCandidate = (id, agentID, metadata) =>
  request("POST", "/v1/candidates", { id, agent_id: agentID, metadata });

export const getCandidate = (id) =>
  request("GET", `/v1/candidates/${segment(id)}`);

export const createRun = (id, candidateID, environment, behavioralProfile) =>
  request("POST", "/v1/evaluation-runs", {
    id,
    candidate_id: candidateID,
    environment,
    behavioral_profile: behavioralProfile,
  });

export const getRun = (id) =>
  request("GET", `/v1/evaluation-runs/${segment(id)}`);

export const getProgress = (id) =>
  request("GET", `/v1/evaluation-runs/${segment(id)}/progress`);

// Lifecycle transitions. Never retried automatically: a repeated start is a
// second attempt, not the same one.
export const startRun = (id) =>
  request("POST", `/v1/evaluation-runs/${segment(id)}/start`);

export const completeRun = (id) =>
  request("POST", `/v1/evaluation-runs/${segment(id)}/complete`);

export const cancelRun = (id) =>
  request("POST", `/v1/evaluation-runs/${segment(id)}/cancel`);

export const failRun = (id, reason) =>
  request("POST", `/v1/evaluation-runs/${segment(id)}/fail`, { reason });

// compare sends the three required limits as canonical decimal strings.
//
// They stay text end to end. Turning them into numbers here would silently
// round a large limit and change the gate the caller asked for.
export const compare = (referenceRunID, candidateRunID, limits) =>
  request("POST", "/v1/evaluations/compare", {
    reference_run_id: referenceRunID,
    candidate_run_id: candidateRunID,
    gate_limits: {
      max_added_behaviors: limits.maxAddedBehaviors,
      max_block_decisions: limits.maxBlockDecisions,
      max_critical_risk_observations: limits.maxCriticalRiskObservations,
    },
  });

// realtimePath builds the SSE URL for one run.
export const realtimePath = (runID) =>
  `/v1/realtime?run_id=${encodeURIComponent(runID)}`;
