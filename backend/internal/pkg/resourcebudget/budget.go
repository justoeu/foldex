package resourcebudget

// BackgroundWorkerConcurrency is the per-pool ceiling for outbound background
// work. Each constructor applies it independently as defense in depth.
const BackgroundWorkerConcurrency = 8
