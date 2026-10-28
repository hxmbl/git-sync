package model

import "time"

type Repository struct {
	Name   string
	Path   string
	GitDir string
	Size   int64
}

type Status int

const (
	StatusUpToDate Status = iota
	StatusUpdated
	StatusSkipped
	StatusFailed
	StatusAhead
)

type UpdateResult struct {
	Repository Repository
	Status     Status
	Detail     string
	Hint       string
	Output     string
	Err        error
	Commits    int
	Remote     string
	Branch     string
	Duration   time.Duration
	DryRun     bool
}

type SyncOptions struct {
	DryRun  bool
	AllRefs bool
	Tags    bool
	Timeout time.Duration
	Verbose bool
}
