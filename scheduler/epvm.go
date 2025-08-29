package scheduler

import (
	"github.com/MHS-20/poseidon/node"
	"github.com/MHS-20/poseidon/task"
)

type Epvm struct {
	Name string
}

func (e *Epvm) SelectCandidateNodes(t task.Task, nodes []*node.Node) []*node.Node {
	var candidates []*node.Node
	for node := range nodes {
		if checkDisk(t, nodes[node].Disk-nodes[node].DiskAllocated) {
			candidates = append(candidates, nodes[node])
		}
	}
	return candidates
}

func checkDisk(t task.Task, diskAvailable int64) bool {
	return t.Disk <= diskAvailable
}
