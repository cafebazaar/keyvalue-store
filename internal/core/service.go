package core

import (
	"bytes"
	"context"
	"sort"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/sirupsen/logrus"

	"github.com/cafebazaar/keyvalue-store/pkg/keyvaluestore"
)

const (
	acceptableDurationDiff = 2 * time.Second
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

	return s.convertErrorToGRPC(s.performWrite(request.Key, request.Options,
		writeOperator, rollbackOperator, keyvaluestore.OperationModeConcurrent))
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

		ttlValue, err := s.engine.Read(args.Winners, s.majority(len(args.Winners)),
			ttlOperator, nil, s.durationComparer, keyvaluestore.VotingModeSkipVoteOnNotFound)
		if err != nil {
			logrus.WithError(err).Error("unexpected error during read repair")
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
		return nil, s.convertErrorToGRPC(err)
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

	return s.convertErrorToGRPC(s.performWrite(request.Key, request.Options,
		writeOperator, rollbackOperator, keyvaluestore.OperationModeConcurrent))
}

func (s *coreService) FlushDB(ctx context.Context) error {
	return s.convertErrorToGRPC(s.performFlushDb())
}

func (s *coreService) Lock(ctx context.Context, request *keyvaluestore.LockRequest) error {
	writeOperator := func(node keyvaluestore.Backend) error {
		return node.Lock(request.Key, request.Data, request.Expiration)
	}

	unlockOperator := func(node keyvaluestore.Backend) error {
		return node.Unlock(request.Key)
	}

	unlockRollbackOperator := func(args keyvaluestore.RollbackArgs) {
	}

	rollbackOperator := func(args keyvaluestore.RollbackArgs) {
		err := s.engine.Write(args.Nodes, 0, unlockOperator, unlockRollbackOperator,
			keyvaluestore.OperationModeConcurrent)

		if err != nil {
			logrus.WithError(err).Error("unexpected error during LOCK rollback")
		}
	}

	// Use sequential (ordered) write sequence to prevent dining philosopher problem
	// (a.k.a chance of deadlock)
	return s.convertErrorToGRPC(s.performWrite(request.Key, request.Options, writeOperator,
		rollbackOperator, keyvaluestore.OperationModeSequential))
}

func (s *coreService) Unlock(ctx context.Context, request *keyvaluestore.UnlockRequest) error {
	writeOperator := func(backend keyvaluestore.Backend) error {
		return backend.Unlock(request.Key)
	}

	rollbackOperator := func(args keyvaluestore.RollbackArgs) {
	}

	return s.convertErrorToGRPC(s.performWrite(request.Key, request.Options, writeOperator,
		rollbackOperator, keyvaluestore.OperationModeConcurrent))
}

func (s *coreService) Expire(ctx context.Context,
	request *keyvaluestore.ExpireRequest) (*keyvaluestore.ExpireResponse, error) {

	readOperator := func(node keyvaluestore.Backend) (interface{}, error) {
		err := node.Expire(request.Key, request.Expiration)
		if err != nil {
			return false, err
		}

		return true, nil
	}

	deleteOperator := func(node keyvaluestore.Backend) error {
		return node.Delete(request.Key)
	}

	deleteRollbackOperator := func(args keyvaluestore.RollbackArgs) {
	}

	ttlOperator := func(node keyvaluestore.Backend) (interface{}, error) {
		return node.TTL(request.Key)
	}

	getOperator := func(node keyvaluestore.Backend) (interface{}, error) {
		return node.Get(request.Key)
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

		ttlValue, err := s.engine.Read(args.Winners, s.majority(len(args.Winners)),
			ttlOperator, nil, s.durationComparer, keyvaluestore.VotingModeSkipVoteOnNotFound)
		if err != nil {
			logrus.WithError(err).Error("unexpected error during read repair")
			return
		}

		var ttl time.Duration
		if args.Value != nil {
			ttl = *(ttlValue.(*time.Duration))
			if ttl == 0 {
				err := s.engine.Write(args.Losers, 0, deleteOperator, deleteRollbackOperator,
					keyvaluestore.OperationModeConcurrent)
				if err != nil {
					logrus.WithError(err).Error("unexpected error during read repair")
				}

				return
			}
		}

		rawValue, err := s.engine.Read(args.Winners, s.majority(len(args.Winners)),
			getOperator, nil, s.byteComparer, keyvaluestore.VotingModeSkipVoteOnNotFound)
		if err != nil {
			logrus.WithError(err).Error("unexpected error during read repair")
			return
		}

		setOperator := func(node keyvaluestore.Backend) error {
			return node.Set(request.Key, rawValue.([]byte), ttl)
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

	rawResult, err := s.performRead(request.Key, keyvaluestore.ReadOptions{
		Consistency: request.Options.Consistency,
	}, readOperator, repairOperator, s.booleanComparer)
	if err != nil {
		if err == keyvaluestore.ErrNotFound {
			return &keyvaluestore.ExpireResponse{Exists: false}, nil
		}

		return nil, s.convertErrorToGRPC(err)
	}

	return &keyvaluestore.ExpireResponse{Exists: rawResult.(bool)}, nil
}

func (s *coreService) Exists(ctx context.Context,
	request *keyvaluestore.ExistsRequest) (*keyvaluestore.ExistsResponse, error) {

	readOperator := func(node keyvaluestore.Backend) (interface{}, error) {
		result, err := node.Exists(request.Key)
		if err != nil {
			return false, err
		}
		if !result {
			return false, keyvaluestore.ErrNotFound
		}

		return true, nil
	}

	deleteOperator := func(node keyvaluestore.Backend) error {
		return node.Delete(request.Key)
	}

	deleteRollbackOperator := func(args keyvaluestore.RollbackArgs) {
	}

	ttlOperator := func(node keyvaluestore.Backend) (interface{}, error) {
		return node.TTL(request.Key)
	}

	getOperator := func(node keyvaluestore.Backend) (interface{}, error) {
		return node.Get(request.Key)
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

		ttlValue, err := s.engine.Read(args.Winners, s.majority(len(args.Winners)),
			ttlOperator, nil, s.durationComparer, keyvaluestore.VotingModeSkipVoteOnNotFound)
		if err != nil {
			logrus.WithError(err).Error("unexpected error during read repair")
			return
		}

		var ttl time.Duration
		if args.Value != nil {
			ttl = *(ttlValue.(*time.Duration))
			if ttl == 0 {
				err := s.engine.Write(args.Losers, 0, deleteOperator, deleteRollbackOperator,
					keyvaluestore.OperationModeConcurrent)
				if err != nil {
					logrus.WithError(err).Error("unexpected error during read repair")
				}

				return
			}
		}

		rawValue, err := s.engine.Read(args.Winners, s.majority(len(args.Winners)),
			getOperator, nil, s.byteComparer, keyvaluestore.VotingModeSkipVoteOnNotFound)
		if err != nil {
			logrus.WithError(err).Error("unexpected error during read repair")
			return
		}

		setOperator := func(node keyvaluestore.Backend) error {
			return node.Set(request.Key, rawValue.([]byte), ttl)
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

	rawResult, err := s.performRead(request.Key, request.Options, readOperator,
		repairOperator, s.booleanComparer)
	if err != nil {
		if err == keyvaluestore.ErrNotFound {
			return &keyvaluestore.ExistsResponse{Exists: false}, nil
		}

		return nil, s.convertErrorToGRPC(err)
	}

	return &keyvaluestore.ExistsResponse{Exists: rawResult.(bool)}, nil
}

// nolint: golint
func (s *coreService) GetTTL(ctx context.Context,
	request *keyvaluestore.GetTTLRequest) (*keyvaluestore.GetTTLResponse, error) {

	readOperator := func(node keyvaluestore.Backend) (interface{}, error) {
		return node.TTL(request.Key)
	}

	deleteOperator := func(node keyvaluestore.Backend) error {
		return node.Delete(request.Key)
	}

	deleteRollbackOperator := func(args keyvaluestore.RollbackArgs) {
	}

	getOperator := func(node keyvaluestore.Backend) (interface{}, error) {
		return node.Get(request.Key)
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

		var ttl time.Duration
		if args.Value != nil {
			ttl = *(args.Value.(*time.Duration))
			if ttl == 0 {
				err := s.engine.Write(args.Losers, 0, deleteOperator, deleteRollbackOperator,
					keyvaluestore.OperationModeConcurrent)
				if err != nil {
					logrus.WithError(err).Error("unexpected error during read repair")
				}

				return
			}
		}

		rawValue, err := s.engine.Read(args.Winners, s.majority(len(args.Winners)),
			getOperator, nil, s.byteComparer, keyvaluestore.VotingModeSkipVoteOnNotFound)
		if err != nil {
			logrus.WithError(err).Error("unexpected error during read repair")
			return
		}

		setOperator := func(node keyvaluestore.Backend) error {
			return node.Set(request.Key, rawValue.([]byte), ttl)
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

	rawResult, err := s.performRead(request.Key, request.Options, readOperator,
		repairOperator, s.durationComparer)
	if err != nil {
		return nil, s.convertErrorToGRPC(err)
	}
	if rawResult == nil {
		return &keyvaluestore.GetTTLResponse{}, nil
	}

	return &keyvaluestore.GetTTLResponse{
		TTL: rawResult.(*time.Duration),
	}, nil
}

func (s *coreService) performWrite(key string,
	options keyvaluestore.WriteOptions,
	operator keyvaluestore.WriteOperator,
	rollback keyvaluestore.RollbackOperator,
	mode keyvaluestore.OperationMode) error {

	consistency := s.writeConsistency(options)
	view, err := s.cluster.Write(key, consistency)
	if err != nil {
		return err
	}

	// Use sequential (ordered)[deterministic order guarantee]
	// write sequence to prevent dining philosopher problem
	// (a.k.a chance of deadlock)
	if mode == keyvaluestore.OperationModeSequential {
		view.Backends = s.sortNodes(view.Backends)
	}

	return s.engine.Write(view.Backends, view.AcknowledgeRequired, operator, rollback, mode)
}

func (s *coreService) performFlushDb() error {

	operator := func(node keyvaluestore.Backend) error {
		return node.FlushDB()
	}

	rollback := func(args keyvaluestore.RollbackArgs) {
	}

	view, err := s.cluster.FlushDB()
	if err != nil {
		return err
	}
	return s.engine.Write(view.Backends, view.AcknowledgeRequired, operator, rollback,
		keyvaluestore.OperationModeConcurrent)
}

func (s *coreService) performRead(key string,
	options keyvaluestore.ReadOptions,
	readOperator keyvaluestore.ReadOperator,
	repairOperator keyvaluestore.RepairOperator,
	comparer keyvaluestore.ValueComparer) (interface{}, error) {

	consistency := s.readConsistency(options)
	view, err := s.cluster.Read(key, consistency)
	if err != nil {
		return nil, err
	}

	return s.engine.Read(view.Backends, view.VoteRequired, readOperator, repairOperator, comparer,
		view.VotingMode)
}

func (s *coreService) sortNodes(nodes []keyvaluestore.Backend) []keyvaluestore.Backend {
	var result []keyvaluestore.Backend
	result = append(result, nodes...)

	sort.Slice(result, func(i, j int) bool {
		return result[i].Address() < result[j].Address()
	})

	return result
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

	diff := *(x.(*time.Duration)) - *(y.(*time.Duration))
	if diff < 0 {
		diff = -diff
	}

	return diff < acceptableDurationDiff
}

func (s *coreService) booleanComparer(x, y interface{}) bool {
	return x.(bool) == y.(bool)
}

func (s *coreService) convertErrorToGRPC(err error) error {
	if err == nil {
		return nil
	}

	switch err {
	case keyvaluestore.ErrNotFound:
		return status.Error(codes.NotFound, keyvaluestore.ErrNotFound.Error())

	case keyvaluestore.ErrConsistency:
		return status.Error(codes.Unavailable, keyvaluestore.ErrConsistency.Error())

	case context.Canceled:
		return status.Error(codes.Canceled, context.Canceled.Error())

	case context.DeadlineExceeded:
		return status.Error(codes.DeadlineExceeded, context.Canceled.Error())

	default:
		return status.Error(codes.Internal, err.Error())
	}
}

func (s *coreService) majority(n int) int {
	return (n / 2) + 1
}
