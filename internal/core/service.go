package core

import (
	"bytes"
	"context"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/cafebazaar/keyvalue-store/pkg/keyvaluestore"
)

type coreService struct {
	cluster                 keyvaluestore.Cluster
	engine                  keyvaluestore.Engine
	defaultWriteConsistency keyvaluestore.ConsistencyLevel
	defaultReadConsistency  keyvaluestore.ConsistencyLevel
}

type Option func(s *coreService)

func New(cluster keyvaluestore.Cluster,
	engine keyvaluestore.Engine,
	options ...Option) keyvaluestore.Service {

	result := &coreService{
		cluster:                 cluster,
		engine:                  engine,
		defaultReadConsistency:  keyvaluestore.ConsistencyLevel_MAJORITY,
		defaultWriteConsistency: keyvaluestore.ConsistencyLevel_ALL,
	}

	for _, option := range options {
		option(result)
	}

	return result
}

func WithDefaultReadConsistency(defaultReadConsistency keyvaluestore.ConsistencyLevel) Option {
	return func(s *coreService) {
		s.defaultReadConsistency = defaultReadConsistency
	}
}

func WithDefaultWriteConsistency(defaultWriteConsistency keyvaluestore.ConsistencyLevel) Option {
	return func(s *coreService) {
		s.defaultWriteConsistency = defaultWriteConsistency
	}
}

func (s *coreService) Set(ctx context.Context, request *keyvaluestore.SetRequest) error {
	writeOperator := func(node keyvaluestore.Backend) error {
		return node.Set(request.Key, request.Data, request.Expiration)
	}

	deleteOperator := func(backend keyvaluestore.Backend) error {
		return backend.Delete(request.Key)
	}

	deleteRollbackOperator := func(args keyvaluestore.RollbackArgs) {
	}

	rollbackOperator := func(args keyvaluestore.RollbackArgs) {
		err := s.engine.Write(args.Nodes, 0, deleteOperator, deleteRollbackOperator,
			keyvaluestore.OperationModeConcurrent)
		if err != nil {
			logrus.WithError(err).Error("unexpected error during SET rollback")
		}
	}

	return s.performWrite(request.Key, request.Options,
		writeOperator, rollbackOperator, keyvaluestore.OperationModeConcurrent)
}

func (s *coreService) Get(ctx context.Context, request *keyvaluestore.GetRequest) (*keyvaluestore.GetResponse, error) {
	readOperator := func(node keyvaluestore.Backend) (interface{}, error) {
		return node.Get(request.Key)
	}

	deleteOperator := func(node keyvaluestore.Backend) error {
		return node.Delete(request.Key)
	}

	deleteRollbackOperator := func(args keyvaluestore.RollbackArgs) {
	}

	ttlOperator := func(node keyvaluestore.Backend) (interface{}, error) {
		return node.TTL(request.Key)
	}

	repairOperator := func(args keyvaluestore.RepairArgs) {
		if args.Err == keyvaluestore.ErrNotFound {
			err := s.engine.Write(args.Losers, 0, deleteOperator, deleteRollbackOperator,
				keyvaluestore.OperationModeConcurrent)
			if err != nil {
				logrus.WithError(err).Error("unexpected error during read repair")
			}

			return
		}

		majority := s.majority(len(args.Winners))
		ttlValue, err := s.engine.Read(args.Winners, majority, ttlOperator, nil, s.durationComparer)
		if err != nil {
			return
		}

		var ttl time.Duration
		shouldRepair := true
		if ttlValue != nil {
			ttl = *(ttlValue.(*time.Duration))
			if ttl == 0 {
				shouldRepair = false
			}
		}

		if shouldRepair {
			setOperator := func(node keyvaluestore.Backend) error {
				return node.Set(request.Key, args.Value.([]byte), ttl)
			}

			setRollbackOperator := func(rollbackArgs keyvaluestore.RollbackArgs) {
				err := s.engine.Write(rollbackArgs.Nodes, 0, deleteOperator, deleteRollbackOperator,
					keyvaluestore.OperationModeConcurrent)
				if err != nil {
					logrus.WithError(err).Error("unexpected error during SET rollback")
				}
			}

			err = s.engine.Write(args.Losers, 0, setOperator, setRollbackOperator, keyvaluestore.OperationModeConcurrent)
			if err != nil {
				logrus.WithError(err).Error("unexpected error during read repair")
			}
		}
	}

	rawResult, err := s.performRead(request.Key, request.Options, readOperator,
		repairOperator, s.byteComparer)
	if err != nil {
		return nil, err
	}

	data := rawResult.([]byte)

	return &keyvaluestore.GetResponse{Data: data}, nil
}

func (s *coreService) Delete(ctx context.Context, request *keyvaluestore.DeleteRequest) error {
	writeOperator := func(node keyvaluestore.Backend) error {
		return node.Delete(request.Key)
	}

	rollbackOperator := func(args keyvaluestore.RollbackArgs) {
	}

	return s.performWrite(request.Key, request.Options,
		writeOperator, rollbackOperator, keyvaluestore.OperationModeConcurrent)
}

func (s *coreService) performWrite(key string,
	options keyvaluestore.WriteOptions,
	operator keyvaluestore.WriteOperator,
	rollback keyvaluestore.RollbackOperator,
	mode keyvaluestore.OperationMode) error {

	consistency := s.writeConsistency(options)
	nodes := s.cluster.WriteBackends(key, consistency)
	acknowledgeCount := s.cluster.WriteAcknowledgeRequired(key, consistency)

	return s.engine.Write(nodes, acknowledgeCount, operator, rollback, mode)
}

func (s *coreService) performRead(key string,
	options keyvaluestore.ReadOptions,
	readOperator keyvaluestore.ReadOperator,
	repairOperator keyvaluestore.RepairOperator,
	comparer keyvaluestore.ValueComparer) (interface{}, error) {

	consistency := s.readConsistency(options)
	nodes := s.cluster.ReadBackends(key, consistency)
	votesRequired := s.cluster.ReadVoteRequired(key, consistency)

	return s.engine.Read(nodes, votesRequired, readOperator, repairOperator, comparer)
}

func (s *coreService) Close() error {
	lastErr := s.cluster.Close()
	if err := s.engine.Close(); err != nil {
		if lastErr != nil {
			logrus.WithError(lastErr).Error("unexpected error while closing core service")
		}

		lastErr = err
	}

	return lastErr
}

func (s *coreService) writeConsistency(writeOptions keyvaluestore.WriteOptions) keyvaluestore.ConsistencyLevel {
	if writeOptions.Consistency == keyvaluestore.ConsistencyLevel_DEFAULT {
		return s.defaultWriteConsistency
	}

	return writeOptions.Consistency
}

func (s *coreService) readConsistency(readOptions keyvaluestore.ReadOptions) keyvaluestore.ConsistencyLevel {
	if readOptions.Consistency == keyvaluestore.ConsistencyLevel_DEFAULT {
		return s.defaultReadConsistency
	}

	return readOptions.Consistency
}

func (s *coreService) byteComparer(x, y interface{}) bool {
	return bytes.Equal(x.([]byte), y.([]byte))
}

func (s *coreService) durationComparer(x, y interface{}) bool {
	if x == nil {
		return y == nil
	}
	if y == nil {
		return false
	}

	return *(x.(*time.Duration)) == *(y.(*time.Duration))
}

func (s *coreService) majority(count int) int {
	return (count / 2) + 1
}
