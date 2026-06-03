package invoke

// RunContext identifies the orchestration slot that produced a role result.
type RunContext struct {
	TaskID       string
	Cycle        int
	Attempt      int
	CycleBaseSHA string
}
