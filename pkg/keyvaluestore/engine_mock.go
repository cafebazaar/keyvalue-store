package keyvaluestore

import "github.com/stretchr/testify/mock"

type Mock_Engine struct {
	mock.Mock
}

func (m *Mock_Engine) Close() error {
	ret := m.Called()

	var r0 error
	if rf, ok := ret.Get(0).(func() error); ok {
		r0 = rf()
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

func (m *Mock_Engine) Read(nodes []Backend, votesRequired int,
	operator ReadOperator, repair RepairOperator,
	cmp ValueComparer, mode VotingMode) (interface{}, error) {

	ret := m.Called(nodes, votesRequired, operator, repair, cmp, mode)

	var r0 interface{}
	if rf, ok := ret.Get(0).(func(nodes []Backend, votesRequired int, operator ReadOperator, repair RepairOperator, cmp ValueComparer, mode VotingMode) interface{}); ok {
		r0 = rf(nodes, votesRequired, operator, repair, cmp, mode)
	} else {
		r0 = ret.Get(0)
	}

	var r1 error
	if rf, ok := ret.Get(1).(func(nodes []Backend, votesRequired int, operator ReadOperator, repair RepairOperator, cmp ValueComparer, mode VotingMode) error); ok {
		r1 = rf(nodes, votesRequired, operator, repair, cmp, mode)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

func (m *Mock_Engine) Write(nodes []Backend, acknowledgeRequired int,
	operator WriteOperator, rollback RollbackOperator, mode OperationMode) error {

	ret := m.Called(nodes, acknowledgeRequired, operator, rollback, mode)

	var r0 error
	if rf, ok := ret.Get(0).(func(nodes []Backend, acknowledgeRequired int, operator WriteOperator, rollback RollbackOperator, mode OperationMode) error); ok {
		r0 = rf(nodes, acknowledgeRequired, operator, rollback, mode)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}
