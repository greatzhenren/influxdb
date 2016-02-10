package continuous_querier

import (
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/influxdata/influxdb/cluster"
	"github.com/influxdata/influxdb/influxql"
	"github.com/influxdata/influxdb/models"
	"github.com/influxdata/influxdb/services/meta"
)

var (
	errExpected   = errors.New("expected error")
	errUnexpected = errors.New("unexpected error")
)

// Test closing never opened, open, open already open, close, and close already closed.
func TestOpenAndClose(t *testing.T) {
	s := NewTestService(t)

	if err := s.Close(); err != nil {
		t.Error(err)
	} else if err = s.Open(); err != nil {
		t.Error(err)
	} else if err = s.Open(); err != nil {
		t.Error(err)
	} else if err = s.Close(); err != nil {
		t.Error(err)
	} else if err = s.Close(); err != nil {
		t.Error(err)
	}
}

// Test Run method.
func TestContinuousQueryService_Run(t *testing.T) {
	s := NewTestService(t)

	// Set RunInterval high so we can trigger using Run method.
	s.RunInterval = 10 * time.Minute

	done := make(chan struct{})
	expectCallCnt := 3
	callCnt := 0

	// Set a callback for ExecuteQuery.
	qe := s.QueryExecutor.(*QueryExecutor)
	qe.ExecuteQueryFn = func(query *influxql.Query, database string, chunkSize int, closing chan struct{}) (<-chan *influxql.Result, error) {
		callCnt++
		if callCnt >= expectCallCnt {
			done <- struct{}{}
		}
		dummych := make(chan *influxql.Result, 1)
		dummych <- &influxql.Result{}
		return dummych, nil
	}

	// Use a custom "now" time since the internals of last run care about
	// what the actual time is. Truncate to 10 minutes we are starting on an interval.
	now := time.Now().Truncate(10 * time.Minute)

	s.Open()
	// Trigger service to run all CQs.
	s.Run("", "", now)
	// Shouldn't time out.
	if err := wait(done, 100*time.Millisecond); err != nil {
		t.Error(err)
	}
	// This time it should timeout because ExecuteQuery should not get called again.
	if err := wait(done, 100*time.Millisecond); err == nil {
		t.Error("too many queries executed")
	}
	s.Close()

	// Now test just one query.
	expectCallCnt = 1
	callCnt = 0
	s.Open()
	s.Run("db", "cq", now)
	// Shouldn't time out.
	if err := wait(done, 100*time.Millisecond); err != nil {
		t.Error(err)
	}
	// This time it should timeout because ExecuteQuery should not get called again.
	if err := wait(done, 100*time.Millisecond); err == nil {
		t.Error("too many queries executed")
	}
	s.Close()
}

func TestContinuousQueryService_ResampleOptions(t *testing.T) {
	s := NewTestService(t)
	mc := NewMetaClient(t)
	mc.CreateDatabase("db", "")
	mc.CreateContinuousQuery("db", "cq", `CREATE CONTINUOUS QUERY cq ON db RESAMPLE EVERY 10s FOR 2m BEGIN SELECT mean(value) INTO cpu_mean FROM cpu GROUP BY time(1m) END`)
	s.MetaClient = mc

	db, err := s.MetaClient.Database("db")
	if err != nil {
		t.Fatal(err)
	}

	cq, err := NewContinuousQuery(db.Name, &db.ContinuousQueries[0])
	if err != nil {
		t.Fatal(err)
	} else if cq.Resample.Every != 10*time.Second {
		t.Errorf("expected resample every to be 10s, got %s", influxql.FormatDuration(cq.Resample.Every))
	} else if cq.Resample.For != 2*time.Minute {
		t.Errorf("expected resample for 2m, got %s", influxql.FormatDuration(cq.Resample.For))
	}

	// Set RunInterval high so we can trigger using Run method.
	s.RunInterval = 10 * time.Minute

	done := make(chan struct{})
	expectCallCnt := 0
	callCnt := 0

	// Set a callback for ExecuteQuery.
	qe := s.QueryExecutor.(*QueryExecutor)
	qe.ExecuteQueryFn = func(query *influxql.Query, database string, chunkSize int, closing chan struct{}) (<-chan *influxql.Result, error) {
		callCnt++
		if callCnt >= expectCallCnt {
			done <- struct{}{}
		}
		dummych := make(chan *influxql.Result, 1)
		dummych <- &influxql.Result{}
		return dummych, nil
	}

	s.Open()
	defer s.Close()

	// Set the 'now' time to the start of a 10 minute interval. Then trigger a run.
	// This should trigger two queries (one for the current time interval, one for the previous).
	now := time.Now().Truncate(10 * time.Minute)
	expectCallCnt += 2
	s.RunCh <- &RunRequest{Now: now}

	if err := wait(done, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	// Trigger another run 10 seconds later. Another two queries should happen,
	// but it will be a different two queries.
	expectCallCnt += 2
	s.RunCh <- &RunRequest{Now: now.Add(10 * time.Second)}

	if err := wait(done, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	// Reset the time period and send the initial request at 5 seconds after the
	// 10 minute mark. There should be exactly one call since the current interval is too
	// young and only one interval matches the FOR duration.
	expectCallCnt += 1
	s.Run("", "", now.Add(5*time.Second))

	if err := wait(done, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	// No overflow should be sent.
	if err := wait(done, 100*time.Millisecond); err == nil {
		t.Error("too many queries executed")
	}
}

func TestContinuousQueryService_EveryHigherThanInterval(t *testing.T) {
	s := NewTestService(t)
	ms := NewMetaClient(t)
	ms.CreateDatabase("db", "")
	ms.CreateContinuousQuery("db", "cq", `CREATE CONTINUOUS QUERY cq ON db RESAMPLE EVERY 1m BEGIN SELECT mean(value) INTO cpu_mean FROM cpu GROUP BY time(30s) END`)
	s.MetaClient = ms

	// Set RunInterval high so we can trigger using Run method.
	s.RunInterval = 10 * time.Minute

	done := make(chan struct{})
	expectCallCnt := 0
	callCnt := 0

	// Set a callback for ExecuteQuery.
	qe := s.QueryExecutor.(*QueryExecutor)
	qe.ExecuteQueryFn = func(query *influxql.Query, database string, chunkSize int, closing chan struct{}) (<-chan *influxql.Result, error) {
		callCnt++
		if callCnt >= expectCallCnt {
			done <- struct{}{}
		}
		dummych := make(chan *influxql.Result, 1)
		dummych <- &influxql.Result{}
		return dummych, nil
	}

	s.Open()
	defer s.Close()

	// Set the 'now' time to the start of a 10 minute interval. Then trigger a run.
	// This should trigger two queries (one for the current time interval, one for the previous)
	// since the default FOR interval should be EVERY, not the GROUP BY interval.
	now := time.Now().Truncate(10 * time.Minute)
	expectCallCnt += 2
	s.RunCh <- &RunRequest{Now: now}

	if err := wait(done, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	// Trigger 30 seconds later. Nothing should run.
	s.RunCh <- &RunRequest{Now: now.Add(30 * time.Second)}

	if err := wait(done, 100*time.Millisecond); err == nil {
		t.Fatal("too many queries")
	}

	// Run again 1 minute later. Another two queries should run.
	expectCallCnt += 2
	s.RunCh <- &RunRequest{Now: now.Add(time.Minute)}

	if err := wait(done, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	// No overflow should be sent.
	if err := wait(done, 100*time.Millisecond); err == nil {
		t.Error("too many queries executed")
	}
}

// Test service when not the cluster leader (CQs shouldn't run).
func TestContinuousQueryService_NotLeader(t *testing.T) {
	s := NewTestService(t)
	// Set RunInterval high so we can test triggering with the RunCh below.
	s.RunInterval = 10 * time.Second
	s.MetaClient.(*MetaClient).Leader = false

	done := make(chan struct{})
	qe := s.QueryExecutor.(*QueryExecutor)
	// Set a callback for ExecuteQuery. Shouldn't get called because we're not the leader.
	qe.ExecuteQueryFn = func(query *influxql.Query, database string, chunkSize int, closing chan struct{}) (<-chan *influxql.Result, error) {
		done <- struct{}{}
		return nil, errUnexpected
	}

	s.Open()
	// Trigger service to run CQs.
	s.RunCh <- &RunRequest{Now: time.Now()}
	// Expect timeout error because ExecuteQuery callback wasn't called.
	if err := wait(done, 100*time.Millisecond); err == nil {
		t.Error(err)
	}
	s.Close()
}

// Test service behavior when meta store fails to get databases.
func TestContinuousQueryService_MetaClientFailsToGetDatabases(t *testing.T) {
	s := NewTestService(t)
	// Set RunInterval high so we can test triggering with the RunCh below.
	s.RunInterval = 10 * time.Second
	s.MetaClient.(*MetaClient).Err = errExpected

	done := make(chan struct{})
	qe := s.QueryExecutor.(*QueryExecutor)
	// Set ExecuteQuery callback, which shouldn't get called because of meta store failure.
	qe.ExecuteQueryFn = func(query *influxql.Query, database string, chunkSize int, closing chan struct{}) (<-chan *influxql.Result, error) {
		done <- struct{}{}
		return nil, errUnexpected
	}

	s.Open()
	// Trigger service to run CQs.
	s.RunCh <- &RunRequest{Now: time.Now()}
	// Expect timeout error because ExecuteQuery callback wasn't called.
	if err := wait(done, 100*time.Millisecond); err == nil {
		t.Error(err)
	}
	s.Close()
}

// Test ExecuteContinuousQuery with invalid queries.
func TestExecuteContinuousQuery_InvalidQueries(t *testing.T) {
	s := NewTestService(t)
	dbis, _ := s.MetaClient.Databases()
	dbi := dbis[0]
	cqi := dbi.ContinuousQueries[0]

	cqi.Query = `this is not a query`
	err := s.ExecuteContinuousQuery(&dbi, &cqi, time.Now())
	if err == nil {
		t.Error("expected error but got nil")
	}

	// Valid query but invalid continuous query.
	cqi.Query = `SELECT * FROM cpu`
	err = s.ExecuteContinuousQuery(&dbi, &cqi, time.Now())
	if err == nil {
		t.Error("expected error but got nil")
	}

	// Group by requires aggregate.
	cqi.Query = `SELECT value INTO other_value FROM cpu WHERE time > now() - 1h GROUP BY time(1s)`
	err = s.ExecuteContinuousQuery(&dbi, &cqi, time.Now())
	if err == nil {
		t.Error("expected error but got nil")
	}
}

// Test ExecuteContinuousQuery when QueryExecutor returns an error.
func TestExecuteContinuousQuery_QueryExecutor_Error(t *testing.T) {
	s := NewTestService(t)
	qe := s.QueryExecutor.(*QueryExecutor)
	qe.Err = errExpected

	dbis, _ := s.MetaClient.Databases()
	dbi := dbis[0]
	cqi := dbi.ContinuousQueries[0]

	now := time.Now().Truncate(10 * time.Minute)
	err := s.ExecuteContinuousQuery(&dbi, &cqi, now)
	if err != errExpected {
		t.Errorf("exp = %s, got = %v", errExpected, err)
	}
}

// NewTestService returns a new *Service with default mock object members.
func NewTestService(t *testing.T) *Service {
	s := NewService(NewConfig())
	ms := NewMetaClient(t)
	s.MetaClient = ms
	s.QueryExecutor = NewQueryExecutor(t)
	s.RunInterval = time.Millisecond

	// Set Logger to write to dev/null so stdout isn't polluted.
	if !testing.Verbose() {
		s.Logger = log.New(ioutil.Discard, "", log.LstdFlags)
	}

	// Add a couple test databases and CQs.
	ms.CreateDatabase("db", "rp")
	ms.CreateContinuousQuery("db", "cq", `CREATE CONTINUOUS QUERY cq ON db BEGIN SELECT count(cpu) INTO cpu_count FROM cpu WHERE time > now() - 1h GROUP BY time(1s) END`)
	ms.CreateDatabase("db2", "default")
	ms.CreateContinuousQuery("db2", "cq2", `CREATE CONTINUOUS QUERY cq2 ON db2 BEGIN SELECT mean(value) INTO cpu_mean FROM cpu WHERE time > now() - 10m GROUP BY time(1m) END`)
	ms.CreateDatabase("db3", "default")
	ms.CreateContinuousQuery("db3", "cq3", `CREATE CONTINUOUS QUERY cq3 ON db3 BEGIN SELECT mean(value) INTO "1hAverages".:MEASUREMENT FROM /cpu[0-9]?/ GROUP BY time(10s) END`)

	return s
}

// MetaClient is a mock meta store.
type MetaClient struct {
	mu            sync.RWMutex
	Leader        bool
	AllowLease    bool
	DatabaseInfos []meta.DatabaseInfo
	Err           error
	t             *testing.T
	nodeID        uint64
}

// NewMetaClient returns a *MetaClient.
func NewMetaClient(t *testing.T) *MetaClient {
	return &MetaClient{
		Leader:     true,
		AllowLease: true,
		t:          t,
		nodeID:     1,
	}
}

// NodeID returns the client's node ID.
func (ms *MetaClient) NodeID() uint64 { return ms.nodeID }

// AcquireLease attempts to acquire the specified lease.
func (ms *MetaClient) AcquireLease(name string) (l *meta.Lease, err error) {
	if ms.Leader {
		if ms.AllowLease {
			return &meta.Lease{Name: name}, nil
		}
		return nil, errors.New("another node owns the lease")
	}
	return nil, meta.ErrServiceUnavailable
}

// Databases returns a list of database info about each database in the cluster.
func (ms *MetaClient) Databases() ([]meta.DatabaseInfo, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.DatabaseInfos, ms.Err
}

// Database returns a single database by name.
func (ms *MetaClient) Database(name string) (*meta.DatabaseInfo, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.database(name)
}

func (ms *MetaClient) database(name string) (*meta.DatabaseInfo, error) {
	if ms.Err != nil {
		return nil, ms.Err
	}
	for i := range ms.DatabaseInfos {
		if ms.DatabaseInfos[i].Name == name {
			return &ms.DatabaseInfos[i], nil
		}
	}
	return nil, fmt.Errorf("database not found: %s", name)
}

// CreateDatabase adds a new database to the meta store.
func (ms *MetaClient) CreateDatabase(name, defaultRetentionPolicy string) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.Err != nil {
		return ms.Err
	}

	// See if the database already exists.
	for _, dbi := range ms.DatabaseInfos {
		if dbi.Name == name {
			return fmt.Errorf("database already exists: %s", name)
		}
	}

	// Create database.
	ms.DatabaseInfos = append(ms.DatabaseInfos, meta.DatabaseInfo{
		Name: name,
		DefaultRetentionPolicy: defaultRetentionPolicy,
	})

	return nil
}

// CreateContinuousQuery adds a CQ to the meta store.
func (ms *MetaClient) CreateContinuousQuery(database, name, query string) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.Err != nil {
		return ms.Err
	}

	dbi, err := ms.database(database)
	if err != nil {
		return err
	} else if dbi == nil {
		return fmt.Errorf("database not found: %s", database)
	}

	// See if CQ already exists.
	for _, cqi := range dbi.ContinuousQueries {
		if cqi.Name == name {
			return fmt.Errorf("continuous query already exists: %s", name)
		}
	}

	// Create a new CQ and store it.
	dbi.ContinuousQueries = append(dbi.ContinuousQueries, meta.ContinuousQueryInfo{
		Name:  name,
		Query: query,
	})

	return nil
}

// QueryExecutor is a mock query executor.
type QueryExecutor struct {
	ExecuteQueryFn func(query *influxql.Query, database string, chunkSize int, closing chan struct{}) (<-chan *influxql.Result, error)
	Results        []*influxql.Result
	ResultInterval time.Duration
	Err            error
	ErrAfterResult int
	t              *testing.T
}

// NewQueryExecutor returns a *QueryExecutor.
func NewQueryExecutor(t *testing.T) *QueryExecutor {
	return &QueryExecutor{
		ErrAfterResult: -1,
		t:              t,
	}
}

// ExecuteQuery returns a channel that the caller can read query results from.
func (qe *QueryExecutor) ExecuteQuery(query *influxql.Query, database string, chunkSize int, closing chan struct{}) (<-chan *influxql.Result, error) {

	// If the test set a callback, call it.
	if qe.ExecuteQueryFn != nil {
		if _, err := qe.ExecuteQueryFn(query, database, chunkSize, make(chan struct{})); err != nil {
			return nil, err
		}
	}

	// Are we supposed to error immediately?
	if qe.ErrAfterResult == -1 && qe.Err != nil {
		return nil, qe.Err
	}

	ch := make(chan *influxql.Result)

	// Start a go routine to send results and / or error.
	go func() {
		n := 0
		for i, r := range qe.Results {
			if i == qe.ErrAfterResult-1 {
				qe.t.Logf("ExecuteQuery(): ErrAfterResult %d", qe.ErrAfterResult-1)
				ch <- &influxql.Result{Err: qe.Err}
				close(ch)
				return
			}
			ch <- r
			n++
			time.Sleep(qe.ResultInterval)
		}
		qe.t.Logf("ExecuteQuery(): all (%d) results sent", n)
		if n == 0 {
			ch <- &influxql.Result{Err: qe.Err}
		}
		close(ch)
	}()

	return ch, nil
}

// PointsWriter is a mock points writer.
type PointsWriter struct {
	WritePointsFn   func(p *cluster.WritePointsRequest) error
	Err             error
	PointsPerSecond int
	t               *testing.T
}

// NewPointsWriter returns a new *PointsWriter.
func NewPointsWriter(t *testing.T) *PointsWriter {
	return &PointsWriter{
		PointsPerSecond: 25000,
		t:               t,
	}
}

// WritePoints mocks writing points.
func (pw *PointsWriter) WritePoints(p *cluster.WritePointsRequest) error {
	// If the test set a callback, call it.
	if pw.WritePointsFn != nil {
		if err := pw.WritePointsFn(p); err != nil {
			return err
		}
	}

	if pw.Err != nil {
		return pw.Err
	}
	ns := time.Duration((1 / pw.PointsPerSecond) * 1000000000)
	time.Sleep(ns)
	return nil
}

// genResult generates a dummy query result.
func genResult(rowCnt, valCnt int) *influxql.Result {
	rows := make(models.Rows, 0, rowCnt)
	now := time.Now()
	for n := 0; n < rowCnt; n++ {
		vals := make([][]interface{}, 0, valCnt)
		for m := 0; m < valCnt; m++ {
			vals = append(vals, []interface{}{now, float64(m)})
			now.Add(time.Second)
		}
		row := &models.Row{
			Name:    "cpu",
			Tags:    map[string]string{"host": "server01"},
			Columns: []string{"time", "value"},
			Values:  vals,
		}
		if len(rows) > 0 {
			row.Name = fmt.Sprintf("cpu%d", len(rows)+1)
		}
		rows = append(rows, row)
	}
	return &influxql.Result{
		Series: rows,
	}
}

func wait(c chan struct{}, d time.Duration) (err error) {
	select {
	case <-c:
	case <-time.After(d):
		err = errors.New("timed out")
	}
	return
}

func waitInt(c chan int, d time.Duration) (i int, err error) {
	select {
	case i = <-c:
	case <-time.After(d):
		err = errors.New("timed out")
	}
	return
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
