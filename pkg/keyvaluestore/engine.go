package keyvaluestore

import (
	"io"
)

type ReadOperator func(backend Backend) (interface{}, error)
type WriteOperator func(backend Backend) error
type RepairOperator func(args RepairArgs)
type RollbackOperator func(args RollbackArgs)

type RepairArgs struct {
	Value   interface{}
	Err     error
	Winners []Backend
	Losers  []Backend
}

type RollbackArgs struct {
	Nodes []Backend
}

type OperationMode int
type VotingMode int

var (
	OperationModeConcurrent OperationMode
	OperationModeSequential OperationMode = 1
)

var (
	VotingModeVoteOnNotFound     VotingMode
	VotingModeSkipVoteOnNotFound VotingMode = 1
)

type Engine interface {
	io.Closer

	Read(nodes []Backend, votesRequired int,
		operator ReadOperator,
		repair RepairOperator,
		cmp ValueComparer,
		mode VotingMode) (interface{}, error)

	Write(nodes []Backend, acknowledgeRequired int,
		operator WriteOperator,
		rollback RollbackOperator,
		mode OperationMode) error
}
