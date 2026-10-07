package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type acknowledgmentConnector struct{ connection *acknowledgmentConnection }

func (c acknowledgmentConnector) Connect(context.Context) (driver.Conn, error) {
	return c.connection, nil
}
func (c acknowledgmentConnector) Driver() driver.Driver { return acknowledgmentDriver{} }

type acknowledgmentDriver struct{}

func (acknowledgmentDriver) Open(string) (driver.Conn, error) { return nil, driver.ErrSkip }

type acknowledgmentConnection struct {
	executed string
	args     []driver.NamedValue
}

func (*acknowledgmentConnection) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (*acknowledgmentConnection) Close() error                        { return nil }
func (*acknowledgmentConnection) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }
func (c *acknowledgmentConnection) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &acknowledgmentRows{}, nil
}
func (c *acknowledgmentConnection) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.executed, c.args = query, args
	return driver.RowsAffected(1), nil
}

type acknowledgmentRows struct{ returned bool }

func (*acknowledgmentRows) Columns() []string { return []string{"count"} }
func (*acknowledgmentRows) Close() error      { return nil }
func (r *acknowledgmentRows) Next(values []driver.Value) error {
	if r.returned {
		return io.EOF
	}
	r.returned = true
	values[0] = int64(1)
	return nil
}

func TestCommandAcknowledgmentQueryAfterCount(t *testing.T) {
	connection := &acknowledgmentConnection{}
	database := sql.OpenDB(acknowledgmentConnector{connection})
	defer database.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: database}), &gorm.Config{
		DisableAutomaticPing: true, SkipDefaultTransaction: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := acknowledgeSessionCommands(db, "session_test", "task_test", []string{"command_test"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(connection.executed, " FROM ") {
		t.Fatalf("acknowledgment reused SELECT clauses: %s", connection.executed)
	}
	for _, scope := range []string{"id IN", "session_id =", "target_id ="} {
		if !strings.Contains(connection.executed, scope) {
			t.Fatalf("missing command ownership scope %s: %s", scope, connection.executed)
		}
	}
	if !strings.HasPrefix(connection.executed, "UPDATE ") {
		t.Fatalf("acknowledgment did not execute UPDATE: %s", connection.executed)
	}
	if !strings.Contains(connection.executed, `"acknowledged_at"`) || !strings.Contains(connection.executed, "acknowledged_at IS NULL") {
		t.Fatalf("incorrect acknowledgment query: %s", connection.executed)
	}
}
