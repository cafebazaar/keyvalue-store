package redis_test

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis"
	redisBackend "github.com/cafebazaar/keyvalue-store/internal/backend/redis"
	"github.com/cafebazaar/keyvalue-store/pkg/keyvaluestore"
	"github.com/go-redis/redis"
	"github.com/stretchr/testify/suite"
)

const (
	KEY    = "key"
	VALUE  = "hello"
	KEY2   = "key2"
	VALUE2 = "hello"
)

type RedisBackendTestSuite struct {
	suite.Suite

	db      *miniredis.Miniredis
	backend keyvaluestore.Backend
}

func TestRedisBackendTestSuite(t *testing.T) {
	suite.Run(t, new(RedisBackendTestSuite))
}

func (s *RedisBackendTestSuite) TestExpireShouldExployExpireOnDatabase() {
	s.NoError(s.db.Set(KEY, VALUE))
	s.NoError(s.backend.Expire(KEY, 1*time.Second))
	s.True(s.db.TTL(KEY) > 500*time.Millisecond)
	s.True(s.db.TTL(KEY) < 1500*time.Millisecond)
}

func (s *RedisBackendTestSuite) TestExpireOnNonExistingKeyShouldReturnErrNotFound() {
	s.Equal(keyvaluestore.ErrNotFound, s.backend.Expire(KEY, 1*time.Second))
}

func (s *RedisBackendTestSuite) TestSetShouldEmployKeyValueOnDatabase() {
	s.Nil(s.backend.Set(KEY, []byte(VALUE), 0))
	s.db.CheckGet(s.T(), KEY, VALUE)
}

func (s *RedisBackendTestSuite) TestSetShouldOverwriteExistingKeyOnDatabase() {
	s.Nil(s.db.Set(KEY, "_"))
	s.Nil(s.backend.Set(KEY, []byte(VALUE), 0))
	s.db.CheckGet(s.T(), KEY, VALUE)
}

func (s *RedisBackendTestSuite) TestSetShouldNotEmployTTLIfNotProvided() {
	s.Nil(s.backend.Set(KEY, []byte(VALUE), 0))
	s.Zero(s.db.TTL(KEY))
}

func (s *RedisBackendTestSuite) TestSetShouldEmployTTLIfProvided() {
	s.Nil(s.backend.Set(KEY, []byte(VALUE), 1*time.Hour))
	ttl := s.db.TTL(KEY)
	s.True(ttl > 59*time.Minute)
	s.True(ttl < 61*time.Minute)
}

func (s *RedisBackendTestSuite) TestSetOnClosedBackendShouldReturnErrClosed() {
	s.Nil(s.backend.Close())
	s.Equal(keyvaluestore.ErrClosed, s.backend.Set(KEY, []byte(VALUE), 0))
}

func (s *RedisBackendTestSuite) TestGetShouldReturnNotFoundIfKeyDoesNotExist() {
	_, err := s.backend.Get(KEY)
	s.Equal(keyvaluestore.ErrNotFound, err)
}

func (s *RedisBackendTestSuite) TestGetShouldReturnValueIfKeyExists() {
	s.Nil(s.db.Set(KEY, VALUE))
	result, err := s.backend.Get(KEY)
	s.Nil(err)
	s.Equal(VALUE, string(result))
}

func (s *RedisBackendTestSuite) TestGetShouldReturnErrClosedIfBackendIsClosed() {
	s.Nil(s.backend.Close())
	_, err := s.backend.Get(KEY)
	s.Equal(keyvaluestore.ErrClosed, err)
}

func (s *RedisBackendTestSuite) TestTTLShouldReturnNotFoundIfKeyDoesNotExist() {
	_, err := s.backend.TTL(KEY)
	s.Equal(keyvaluestore.ErrNotFound, err)
}

func (s *RedisBackendTestSuite) TestTTLShouldReturnZeroIfTTLIsNotEmployed() {
	s.Nil(s.db.Set(KEY, VALUE))
	result, err := s.backend.TTL(KEY)
	s.Nil(err)
	s.Zero(result)
}

func (s *RedisBackendTestSuite) TestTTLShouldReturnCoorectTTLIfEmployed() {
	s.Nil(s.db.Set(KEY, VALUE))
	s.db.SetTTL(KEY, 1*time.Hour)
	result, err := s.backend.TTL(KEY)
	s.Nil(err)
	s.NotNil(result)
	if result != nil {
		s.True(*result > 59*time.Minute)
		s.True(*result < 61*time.Minute)
	}
}

func (s *RedisBackendTestSuite) TestDeleteShouldSucceedIfKeyDoesNotExist() {
	s.Nil(s.backend.Delete(KEY))
}

func (s *RedisBackendTestSuite) TestDeleteShouldDeleteExistingKey() {
	s.Nil(s.db.Set(KEY, VALUE))
	s.Nil(s.backend.Delete(KEY))
	s.False(s.db.Exists(KEY))
}

func (s *RedisBackendTestSuite) TestFlushDbShouldDeleteAllKeys() {
	s.Nil(s.db.Set(KEY, VALUE))
	s.Nil(s.db.Set(KEY2, VALUE2))
	s.Nil(s.backend.FlushDB())
	s.False(s.db.Exists(KEY))
	s.False(s.db.Exists(KEY2))
}

func (s *RedisBackendTestSuite) TestAddressShouldReturnCorrectAddress() {
	s.Equal("localhost", s.backend.Address())
}

func (s *RedisBackendTestSuite) TestLockShouldSucceedOnCleanDatabase() {
	s.Nil(s.backend.Lock(KEY, []byte("-"), 1*time.Second))
	s.True(s.db.Exists(KEY))
}

func (s *RedisBackendTestSuite) TestUnlockShouldReleasePreviouslyLockedKey() {
	s.Nil(s.backend.Lock(KEY, []byte("-"), 1*time.Second))
	s.Nil(s.backend.Unlock(KEY))
	s.False(s.db.Exists(KEY))
}

func (s *RedisBackendTestSuite) TestConsecutiveLockShouldFail() {
	s.Nil(s.backend.Lock(KEY, []byte("-"), 1*time.Second))
	s.Equal(keyvaluestore.ErrNotAcquired, s.backend.Lock(KEY, []byte("-"), 1*time.Second))
}

func (s *RedisBackendTestSuite) TestAfterUnlockShouldBeLockable() {
	s.Nil(s.backend.Lock(KEY, []byte("-"), 1*time.Second))
	s.Nil(s.backend.Unlock(KEY))
	s.Nil(s.backend.Lock(KEY, []byte("-"), 1*time.Second))
}

func (s *RedisBackendTestSuite) TestExistsShouldReturnTrueForExistingKey() {
	s.Nil(s.db.Set(KEY, VALUE))
	exists, err := s.backend.Exists(KEY)
	s.Nil(err)
	s.True(exists)
}

func (s *RedisBackendTestSuite) TestExistsShouldReturnTrueForNonExistingKey() {
	exists, err := s.backend.Exists(KEY)
	s.Nil(err)
	s.False(exists)
}

func (s *RedisBackendTestSuite) SetupTest() {
	var err error

	s.db, err = miniredis.Run()
	if err != nil {
		s.FailNow("failed to create miniredis db")
	}

	client := redis.NewClient(&redis.Options{Addr: s.db.Addr()})

	s.backend = redisBackend.New(client, "localhost")
}

func (s *RedisBackendTestSuite) TearDownTest() {
	err := s.backend.Close()
	if err != nil {
		s.FailNow("failed to close backend")
	}

	s.db.Close()
}
