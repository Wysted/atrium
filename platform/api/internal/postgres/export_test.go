package postgres

// HousekeepingLockKey exposes the housekeeping advisory lock key to the external tests.
const HousekeepingLockKey = housekeepingLockKey

// CloseBounded and ConnCloseTimeout expose the bounded close of LISTEN connections to the external tests.
var CloseBounded = closeBounded

const ConnCloseTimeout = connCloseTimeout
