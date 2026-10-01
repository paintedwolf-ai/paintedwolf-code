package readcaps

// LineLimit caps read pagination for workers and coordinators. It bounds the
// lines returned, not the bytes ingested — MaxFileBytes sets that.
const LineLimit = 2000

// MaxFileBytes matches the per-file search limit so searchable files remain readable.
const MaxFileBytes = 8 << 20

// MaxMutationBytes bounds native text tool mutations, matching blob retention.
const MaxMutationBytes = 4 << 20

// AutoOutlineThreshold triggers outline-only read when mode is omitted and the
// caller did not request pagination (no offset/limit/ranges args).
const AutoOutlineThreshold = LineLimit

// BatchMaxRanges caps read(path, ranges=[…]) arity.
const BatchMaxRanges = 8

// BatchMaxTotalLines caps total lines across all ranges in one read call.
const BatchMaxTotalLines = LineLimit * 2
