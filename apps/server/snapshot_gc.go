package main

type snapshotRetentionReport struct {
	Snapshots        int                 `json:"snapshots"`
	Retained         int                 `json:"retained"`
	Candidates       int                 `json:"candidates"`
	Reclaimed        int                 `json:"reclaimed"`
	ReclaimableBytes int64               `json:"reclaimable_bytes"`
	SharedBytes      int64               `json:"shared_bytes"`
	QueuedBytes      int64               `json:"queued_bytes"`
	Roots            map[string][]string `json:"roots"`
}
