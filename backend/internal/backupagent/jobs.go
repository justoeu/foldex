package backupagent

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/backupjobs"
)

// Shared schedule vocabulary lives in backupjobs. These aliases keep the
// agent package's existing call sites (tests, cmd/backup-agent) compiling
// without a second find-replace of every JobDump / Artifact identifier.

const (
	JobDump    = backupjobs.JobDump
	JobMirror  = backupjobs.JobMirror
	JobUserZip = backupjobs.JobUserZip
	JobDrill   = backupjobs.JobDrill

	RequiredSchemaVersion = backupjobs.RequiredSchemaVersion

	MinTimes        = backupjobs.MinTimes
	MaxTimes        = backupjobs.MaxTimes
	MinWeekdays     = backupjobs.MinWeekdays
	MinDumpWeekdays = backupjobs.MinDumpWeekdays
	MinIntervalMin  = backupjobs.MinIntervalMin
	MaxIntervalMin  = backupjobs.MaxIntervalMin

	ReasonDumpFailed    = backupjobs.ReasonDumpFailed
	ReasonUserZipFailed = backupjobs.ReasonUserZipFailed
	ReasonEncryptFailed = backupjobs.ReasonEncryptFailed
	ReasonSpoolFailed   = backupjobs.ReasonSpoolFailed
	ReasonUploadFailed  = backupjobs.ReasonUploadFailed
	ReasonPruneFailed   = backupjobs.ReasonPruneFailed
	ReasonStaleClaim    = backupjobs.ReasonStaleClaim
	ReasonShutdown      = backupjobs.ReasonShutdown
	ReasonLockBusy      = backupjobs.ReasonLockBusy

	ReasonDrillSourceFailed   = backupjobs.ReasonDrillSourceFailed
	ReasonDrillNoDump         = backupjobs.ReasonDrillNoDump
	ReasonDrillDownloadFailed = backupjobs.ReasonDrillDownloadFailed
	ReasonDrillDigestMismatch = backupjobs.ReasonDrillDigestMismatch
	ReasonDrillDecryptFailed  = backupjobs.ReasonDrillDecryptFailed
	ReasonDrillRestoreFailed  = backupjobs.ReasonDrillRestoreFailed
	ReasonDrillCountsMismatch = backupjobs.ReasonDrillCountsMismatch

	ReasonRestoreInFlight  = backupjobs.ReasonRestoreInFlight
	ReasonMirrorScanFailed = backupjobs.ReasonMirrorScanFailed
	ReasonMirrorCopyFailed = backupjobs.ReasonMirrorCopyFailed
)

type (
	Artifact      = backupjobs.Artifact
	MirrorStats   = backupjobs.MirrorStats
	RunStore      = backupjobs.RunStore
	DumpRunRef    = backupjobs.DumpRunRef
	JobConfig     = backupjobs.JobConfig
	Timing        = backupjobs.Timing
	ScheduleStore = backupjobs.ScheduleStore
	ScheduleRow   = backupjobs.ScheduleRow
	JobReport     = backupjobs.JobReport
	Destination   = backupjobs.Destination
	AgentState    = backupjobs.AgentState
	Anchor        = backupjobs.Anchor
)

var (
	ErrAlreadyRunning = backupjobs.ErrAlreadyRunning
	ErrRunNotRunning  = backupjobs.ErrRunNotRunning
	ErrNoDumpToDrill  = backupjobs.ErrNoDumpToDrill
)

func NewRunStore(pool *pgxpool.Pool) *RunStore {
	return backupjobs.NewRunStore(pool)
}

func NewScheduleStore(pool *pgxpool.Pool) *ScheduleStore {
	return backupjobs.NewScheduleStore(pool)
}

func ValidateJobConfig(job string, cfg JobConfig) error {
	return backupjobs.ValidateJobConfig(job, cfg)
}

func ParseAnchor(raw string) (Anchor, error) {
	return backupjobs.ParseAnchor(raw)
}

func TimingFromConfig(cfg JobConfig) Timing {
	return backupjobs.TimingFromConfig(cfg)
}
