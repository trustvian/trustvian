// Pausing the Live view (task 109, decisions 5 and 6).
//
// Pause freezes what the view draws from frames and keeps the connection.
// Observation frames that arrive while paused are kept here, in order, up to a
// fixed bound; resume folds them into the model without animation. Nothing is
// fetched to reconstruct history and nothing is replayed: a pulse reports that
// a frame arrived now, and these did not.
//
// Ephemeral, like follow and pin: nothing here is persisted, and a reload
// starts live.

// PAUSE_BUFFER_MAX bounds the frames kept while paused. Measured, not chosen
// for comfort: the quickstarts' three-worker fleet produced about 3 frames a
// second, so 1,024 is about five minutes of it and a 30-second pause is about
// 90.
export const PAUSE_BUFFER_MAX = 1024;

// PauseBuffer holds the pause state and the frames kept during it.
//
// Its counts describe this connection's frames, and are labelled that way
// wherever they are shown: "seen live, all runs", and "in this run".
export class PauseBuffer {
  constructor(capacity = PAUSE_BUFFER_MAX) {
    this.capacity = capacity;
    this.paused = false;
    this.clearState();
  }

  clearState() {
    this.frames = [];
    // Frames that arrived since pause, kept or not, and how many of them
    // belonged to the run the view was drawing when they arrived.
    this.sincePause = 0;
    this.inRun = 0;
    // The buffer filled and was discarded: resume starts the view again from
    // now rather than draw part of the pause as though it were all of it.
    this.overrun = false;
    // The stream reconnected while paused. Frames from two connections cannot
    // be joined, so the frozen picture is the previous connection's and resume
    // applies the reset a connect would have applied.
    this.reconnected = false;
  }

  pause() {
    this.paused = true;
    this.clearState();
  }

  // add keeps one observation frame. Past the bound the buffer is discarded
  // and counting continues — the rule the resync buffer already follows:
  // abandon and start again rather than drop a frame and draw the rest as
  // complete.
  add(frame, inSelectedRun) {
    this.sincePause += 1;
    if (inSelectedRun) {
      this.inRun += 1;
    }
    if (this.overrun) {
      return;
    }
    if (this.frames.length >= this.capacity) {
      this.overrun = true;
      this.frames = [];
      return;
    }
    this.frames.push(frame);
  }

  // connectionChanged discards what the previous connection buffered, and
  // buffering restarts on the new one.
  connectionChanged() {
    this.frames = [];
    this.overrun = false;
    this.reconnected = true;
  }

  // resume ends the pause and hands back what it kept.
  resume() {
    const result = {
      frames: this.frames,
      sincePause: this.sincePause,
      overrun: this.overrun,
      reconnected: this.reconnected,
    };
    this.paused = false;
    this.clearState();
    return result;
  }
}

// isTypingTarget reports whether a key press belongs to a text field, where
// the pause shortcut must never fire.
export function isTypingTarget(target) {
  if (target === null || target === undefined) {
    return false;
  }
  const tag = typeof target.tagName === "string" ? target.tagName.toUpperCase() : "";
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || target.isContentEditable === true;
}
