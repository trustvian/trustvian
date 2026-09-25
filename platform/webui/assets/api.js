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
        //
        // cancel() is awaited but its failure is swallowed on purpose. On an
        // already-errored stream it can reject, and letting that propagate
        // would replace "the response was too large" with whatever the stream
        // failed with — reporting the wrong reason for a refusal we already
        // decided on.
        try {
          await reader.cancel();
        } catch (ignored) {
          // The transfer is over either way.
        }
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

// operationalFrom converts a thrown transport failure into an ApiError.
//
// Two outcomes only, and the distinction is what the caller needs: the deadline
// expired, or the control plane could not be reached. Neither is a server
// answer, so both are operational and neither may be presented as a gate
// result.
//
// A raw AbortError or DOMException never reaches the UI. Recognising it here is
// the only place that has the context to say what the abort meant.
function operationalFrom(cause, deadlineExpired) {
  const aborted = deadlineExpired ||
    (cause !== null && cause !== undefined && cause.name === "AbortError");
  return new ApiError(
    aborted
      ? `no response within ${REQUEST_TIMEOUT_MS}ms`
      : "the control plane could not be reached",
    { operational: true },
  );
}

// request issues one bounded, same-origin call.
//
// Relative URLs only: the UI is served from the same origin as the API, so
// there is no endpoint to configure and nothing that could point this at
// another host.
//
// The deadline covers the **whole** operation — request transmission, response
// headers, and bounded consumption of the body — and not merely the fetch.
//
// fetch() resolves as soon as headers are available, while the body may still
// be streaming or stalled. A deadline released at that point leaves the read
// below unbounded, and a server that sends headers promptly and then stops
// sending bytes would hang the operation forever. The 4 MiB bound does not
// help: a stalled body never reaches it.
//
// Extending the controller over the read is what actually stops it. Per the
// Fetch standard, aborting after headers have arrived errors the response body
// stream, so the pending read rejects with an AbortError rather than staying
// blocked — which is why this is a real bound and not a hopeful one.
//
// One AbortController, one timer, cleared in exactly one place: a finally that
// encloses both the fetch and the body read. Moving the clear earlier is the
// regression this shape exists to prevent, and
// TestRequestDeadlineCoversBodyConsumption pins the ordering.
async function request(method, path, body) {
  const controller = new AbortController();
  // Our own deadline, distinguished from any other abort so the reason reported
  // is right without inspecting DOMException internals.
  let deadlineExpired = false;
  const timer = setTimeout(() => {
    deadlineExpired = true;
    controller.abort();
  }, REQUEST_TIMEOUT_MS);

  try {
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
      throw operationalFrom(cause, deadlineExpired);
    }

    let text;
    try {
      text = await readBounded(response);
    } catch (cause) {
      // An over-limit body already arrives as an ApiError and keeps its own
      // meaning. Reclassifying it here would report a size refusal as a
      // timeout, and the two call for different fixes.
      if (cause instanceof ApiError) {
        throw cause;
      }
      // Anything else from the stream — including the deadline firing
      // mid-transfer — is operational. It is not malformed JSON, not an HTTP
      // refusal, and not a gate result.
      throw operationalFrom(cause, deadlineExpired);
    }

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
  } finally {
    // Every path above leaves through here: success, request too large, HTTP
    // refusal, malformed JSON, size overflow, transport failure and timeout.
    clearTimeout(timer);
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
// Routes. Every one of these is a /v1 route the server publishes; this module
// adds no capability of its own.
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

// createPromotion records one decision.
//
// The body carries only what the caller owns: an identifier, two completed
// runs, a target environment and the three limits. The source environment, the
// project, the candidate, the gate verdict and the outcome are all derived by
// the server, and sending any of them is a 400 because /v1 decodes strictly.
// That is the point — a browser cannot express an outcome it did not earn.
export const createPromotion = (id, referenceRunID, candidateRunID, target, limits) =>
  request("POST", "/v1/promotions", {
    id,
    reference_run_id: referenceRunID,
    candidate_run_id: candidateRunID,
    target_environment: target,
    gate_limits: {
      max_added_behaviors: limits.maxAddedBehaviors,
      max_block_decisions: limits.maxBlockDecisions,
      max_critical_risk_observations: limits.maxCriticalRiskObservations,
    },
  });

// getPromotion reads one recorded decision.
export const getPromotion = (id) =>
  request("GET", `/v1/promotions/${segment(id)}`);

// listPromotions reads one bounded page of a project's history.
//
// One page, and the caller decides whether to ask for another. `after` is the
// exclusive cursor the previous page published as `next_after`.
export const listPromotions = (projectID, after) => {
  const query = after ? `?after=${encodeURIComponent(after)}` : "";
  return request("GET", `/v1/projects/${segment(projectID)}/promotions${query}`);
};

// listEnvironments reads one bounded page of a project's environments.
//
// The promotion form uses it to offer targets rather than asking a person to
// type a ref. Which of them are promotable is the server's answer, not this
// page's: nothing here compares a rank.
export const listEnvironments = (projectID, after) => {
  const query = after ? `?after=${encodeURIComponent(after)}` : "";
  return request("GET", `/v1/projects/${segment(projectID)}/environments${query}`);
};

// MAX_ENVIRONMENT_PAGES bounds the target-picker traversal below.
//
// A browser-only defensive ceiling, not a product rule. Task 065's cap of 64
// environments per project governs *creation, not existence*: a database
// migrated from schema 2 backfills one environment per distinct reference its
// runs recorded, so a valid project may legitimately hold more than 64 and a
// promotable target may sit on page 2. That is exactly why this traverses
// instead of reading page one.
//
// But "more than the cap" is not "without limit" as far as this page is
// concerned. The server bounds each page at 64 rows, so 64 pages is 4096
// environments — sixty-four times what any project may now create, and far
// past any migrated history a person could pick from a dropdown. Reaching it
// means the server is not terminating the collection, and the operation is
// refused rather than allowed to grow a list forever.
//
// The server's collection contract stays authoritative. Nothing here asks for
// a larger page, and nothing here decides an environment is uninteresting.
export const MAX_ENVIRONMENT_PAGES = 64;

// listAllEnvironments traverses one project's bounded environment collection.
//
// The target picker is the one place the browser needs the *whole* collection
// rather than a page: a person choosing a promotion target has to be able to
// choose one that a migrated database put on page 2. So this follows
// `next_after` to completion, and the rows live only for the duration of the
// pick.
//
// Promotion history deliberately does **not** use this. History is navigated a
// page at a time, because it grows without bound and a browser that assembled
// all of it would be an unbounded accumulator with a nicer name.
//
// Cursor safety mirrors the CLI's validatePromotionCursor: each condition
// below is impossible from a correct server and non-terminating if believed,
// so each is refused rather than followed.
export async function listAllEnvironments(projectID) {
  const environments = [];
  let after = "";
  let version;
  let scope;

  for (let page = 0; page < MAX_ENVIRONMENT_PAGES; page++) {
    const response = await listEnvironments(projectID, after);
    const rows = Array.isArray(response.environments) ? response.environments : [];

    // The envelope must keep describing the same collection. A version or
    // project that changes mid-traversal means the pages are not one listing,
    // and merging them would present a set that never existed.
    if (page === 0) {
      version = response.version;
      scope = response.project_id;
    } else if (response.version !== version || response.project_id !== scope) {
      throw new ApiError(
        "the environment listing changed identity mid-traversal",
        { operational: true },
      );
    }

    for (const row of rows) {
      environments.push(row);
    }

    const next = typeof response.next_after === "string" ? response.next_after : "";
    if (next === "") {
      return { version, project_id: scope, environments };
    }

    // A continuation that cannot make progress. Believing any of these loops
    // forever, so each is reported as the server fault it is.
    if (rows.length === 0) {
      throw new ApiError(
        `the server offered cursor "${next}" on an empty page of environments`,
        { operational: true },
      );
    }
    if (after !== "" && next <= after) {
      throw new ApiError(
        `the environment listing cursor did not advance: "${next}" followed "${after}"`,
        { operational: true },
      );
    }
    const last = rows[rows.length - 1];
    if (!last || last.ref !== next) {
      throw new ApiError(
        `the environment listing cursor "${next}" is not the page's last environment`,
        { operational: true },
      );
    }
    after = next;
  }

  // Deliberately an error rather than a truncated list. Returning what fitted
  // would offer a target picker that silently omits valid targets, and a
  // person would read the absence as "that environment does not exist".
  throw new ApiError(
    `too many environment pages were returned to display safely (over ${MAX_ENVIRONMENT_PAGES})`,
    { operational: true },
  );
}

// promotionCursorFault reports why a promotion continuation cannot be followed,
// or "" when it can.
//
// History is paged rather than traversed, so the check runs per click instead
// of inside a loop — but it is the same rule the environment traversal and the
// CLI apply, and for the same reason: a cursor that cannot advance is a server
// fault, and following it is how a "Load next page" button becomes infinite.
export function promotionCursorFault(previous, response) {
  const next = typeof response.next_after === "string" ? response.next_after : "";
  if (next === "") {
    return "";
  }
  const rows = Array.isArray(response.promotions) ? response.promotions : [];
  if (rows.length === 0) {
    return `the server offered cursor "${next}" on an empty page of decisions`;
  }
  if (previous !== "" && next <= previous) {
    return `the promotion history cursor did not advance: "${next}" followed "${previous}"`;
  }
  const last = rows[rows.length - 1];
  if (!last || last.id !== next) {
    return `the promotion history cursor "${next}" is not the page's last decision`;
  }
  return "";
}

// realtimePath builds the SSE URL.
//
// An empty runID produces `/v1/realtime` with no query at all, which the
// server already defines as unconstrained — RealtimeFilter treats every empty
// dimension as "no constraint", and its doc comment says so. That is the Live
// view's subscription: no route change, no new parameter, no new event kind.
// Sending `run_id=` explicitly would be the same thing to the server and a
// worse thing to read in a log.
export const realtimePath = (runID) =>
  runID ? `/v1/realtime?run_id=${encodeURIComponent(runID)}` : "/v1/realtime";

// ---------------------------------------------------------------------
// Hierarchy collections (task 074)
// ---------------------------------------------------------------------

// Four bounded pages, one per level. Each takes an exclusive `after` cursor
// and returns at most MaxListPage rows with `next_after` present exactly when
// another row follows.
//
// Deliberately four plain functions rather than one generic pager: the paths
// differ, the envelope keys differ, and a generic pagination layer would be an
// abstraction over four call sites. There is also no `listAll…` traversal for
// any of them — the Live view's whole discovery budget is one page of
// projects, and a helper that walked a level would make an unbounded workflow
// one keystroke away.
export const listProjects = (after) => {
  const query = after ? `?after=${encodeURIComponent(after)}` : "";
  return request("GET", `/v1/projects${query}`);
};

export const listProjectAgents = (projectID, after) => {
  const query = after ? `?after=${encodeURIComponent(after)}` : "";
  return request("GET", `/v1/projects/${segment(projectID)}/agents${query}`);
};

export const listAgentCandidates = (agentID, after) => {
  const query = after ? `?after=${encodeURIComponent(after)}` : "";
  return request("GET", `/v1/agents/${segment(agentID)}/candidates${query}`);
};

export const listCandidateRuns = (candidateID, after) => {
  const query = after ? `?after=${encodeURIComponent(after)}` : "";
  return request("GET", `/v1/candidates/${segment(candidateID)}/evaluation-runs${query}`);
};
