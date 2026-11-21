package toolcontract

import "time"

// ProgramParkThreshold bounds a known inline wait or the initial job-only
// wait; a longer wait parks the call and the program runs on in the background.
const ProgramParkThreshold = 10 * time.Second

// ProgramInlineWait bounds how long an unparked program holds the agent before
// the call returns a running handle.
const ProgramInlineWait = 30 * time.Second
