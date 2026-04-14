package workspaceops

func ReadIssueWork(dataDir string, workspaceID string, issueID string, runbookID string) (*IssueContextPacket, error) {
	return BuildIssueWorkPacket(dataDir, workspaceID, issueID, runbookID)
}
