package db

// OnStatusChange, when set, is invoked after a machine or tunnel status
// actually transitions (old != new), and on create/delete of either. Wired
// once at startup (cmd/server) to the StatusHub so dashboards get push
// updates instead of waiting for their poll interval. Invoked synchronously
// from the writer's goroutine — handlers must not block.
var OnStatusChange func(kind, id, status string)

func notifyStatusChange(kind, id, old, status string) {
	if OnStatusChange != nil && old != status {
		OnStatusChange(kind, id, status)
	}
}

// currentMachineStatus reads just the status column for change detection.
// Returns "" when the row is missing — the subsequent notify then fires,
// which is the right call for a first-ever status write.
func currentMachineStatus(id string) string {
	var m Machine
	_ = DB.Select("status").Where("id = ?", id).Take(&m).Error
	return m.Status
}

func currentTunnelStatus(id string) string {
	var t Tunnel
	_ = DB.Select("status").Where("id = ?", id).Take(&t).Error
	return t.Status
}
