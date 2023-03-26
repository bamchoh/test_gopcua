package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"
)

type AlarmInfo struct {
	Id        int    `json:"id"`
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Timestamp int    `json:"timestamp"`
	Value     int    `json:"value"`
}

type Dao interface {
	InitVariables(nodeIds []string) error

	SetIsReady()

	Alarms() ([]AlarmInfo, error)
}

type MyDatabase struct {
	*sql.DB
	isReady chan struct{}
}

func createSql() (*MyDatabase, error) {
	os.Remove("./database.db")

	db, err := sql.Open("sqlite3", "./foo.db")
	if err != nil {
		return nil, fmt.Errorf("db open failed: %w", err)
	}

	sqlStmt := `
	create table if not exists variables (
		nodeid text not null primary key,
		value string
	);
	delete from variables;
	`
	_, err = db.Exec(sqlStmt)
	if err != nil {
		err = fmt.Errorf("%w: %s\n", err, sqlStmt)
		return nil, err
	}

	sqlStmt = `
	create table if not exists alarms (
		id integer primary key autoincrement,
		code integer,
		message text,
		timestamp integer,
		value integer
	);
	delete from alarms;
	`
	_, err = db.Exec(sqlStmt)
	if err != nil {
		err = fmt.Errorf("%w: %s\n", err, sqlStmt)
		return nil, err
	}

	return &MyDatabase{
		DB:      db,
		isReady: make(chan struct{}),
	}, nil
}

func (db *MyDatabase) InitVariables(nodeIds []string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("transaction could not begin: %w", err)
	}
	stmt, err := tx.Prepare("insert into variables(nodeid, value) values(?, ?)")
	if err != nil {
		return fmt.Errorf("transaction preparation was failed: %w", err)
	}
	defer stmt.Close()

	for _, nodeId := range nodeIds {
		_, err = stmt.Exec(nodeId, "0")
		if err != nil {
			return fmt.Errorf("nodeid cannot be inserted: %w", err)
		}
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("transaction could not commit: %w", err)
	}

	return nil
}

func (db *MyDatabase) SetIsReady() {
	db.isReady <- struct{}{}
}

func (db *MyDatabase) Alarms() ([]AlarmInfo, error) {
	rows, err := db.Query("select id, code, message, timestamp, value from alarms order by id desc limit 10")
	if err != nil {
		return nil, fmt.Errorf("alarms query was failed: %w", err)
	}
	defer rows.Close()

	alarms := make([]AlarmInfo, 0)

	for rows.Next() {
		var id int
		var code int
		var message string
		var timestamp int
		var value int
		err = rows.Scan(&id, &code, &message, &timestamp, &value)
		if err != nil {
			return nil, fmt.Errorf("alarms rows scan was failed: %w", err)
		}
		alarms = append(alarms, AlarmInfo{
			Id:        id,
			Code:      code,
			Message:   message,
			Timestamp: timestamp,
			Value:     value,
		})
	}

	err = rows.Err()
	if err != nil {
		return nil, fmt.Errorf("rows err was failed: %w", err)
	}

	return alarms, nil
}

func (db *MyDatabase) getCount() (int, error) {
	countNodeID := "ns=2;s=Root.Count"
	var count int

	rows, err := db.Query("select value from variables where nodeid = '" + countNodeID + "'")
	if err != nil {
		return 0, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		err = rows.Scan(&count)
		if err != nil {
			return 0, fmt.Errorf("row scan failed: %w", err)
		}
	}

	err = rows.Err()
	if err != nil {
		return 0, fmt.Errorf("rows failed: %w", err)
	}

	return count, nil
}

func (db *MyDatabase) readAlarm() error {
	codeNodeId := "ns=2;s=Root.Code"
	messageNodeId := "ns=2;s=Root.Message"
	timestampNodeId := "ns=2;s=Root.Timestamp"
	valueNodeId := "ns=2;s=Root.Value"
	rows, err := db.Query("select nodeid, value from variables where nodeid in ($1, $2, $3, $4)",
		codeNodeId, messageNodeId, timestampNodeId, valueNodeId,
	)

	if err != nil {
		return fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	values := make(map[string]string)
	for rows.Next() {
		var nodeid string
		var value string
		err = rows.Scan(&nodeid, &value)
		if err != nil {
			return fmt.Errorf("row scan failed: %w", err)
		}
		values[nodeid] = value
	}

	err = rows.Err()
	if err != nil {
		return fmt.Errorf("rows failed: %w", err)
	}

	_, err = db.Exec(`insert into alarms(code, message, timestamp, value) values (?, ?, ?, ?)`,
		values[codeNodeId],
		values[messageNodeId],
		values[timestampNodeId],
		values[valueNodeId],
	)

	if err != nil {
		return fmt.Errorf("nodeid cannot be inserted: %w", err)
	}

	return nil
}

func (db *MyDatabase) Main(ctx context.Context, toOpc chan Message, fromOpc chan Message) error {
	select {
	case <-ctx.Done():
		return errors.New("other goroutine was ended")
	case <-db.isReady:
	}

	messageLoopErr := make(chan error)

	go func() {
		for {
			select {
			case msgif := <-fromOpc:
				switch msg := msgif.(type) {
				case *UpdateVariablesMessage:
					tx, err := db.Begin()
					if err != nil {
						messageLoopErr <- fmt.Errorf("update transaction begin was failed: %w", err)
						return
					}
					stmt, err := tx.Prepare("update variables set value = ? where nodeid = ?")
					if err != nil {
						messageLoopErr <- fmt.Errorf("update transaction prepare was failed: %w", err)
						return
					}
					defer stmt.Close()

					for i := 0; i < len(msg.NodeIds); i++ {
						_, err = stmt.Exec(msg.Values[i], msg.NodeIds[i])
						if err != nil {
							messageLoopErr <- fmt.Errorf("update transaction exec was failed: %w", err)
							return
						}
					}

					err = tx.Commit()
					if err != nil {
						messageLoopErr <- fmt.Errorf("update transaction commit was failed: %w", err)
						return
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		count, err := db.getCount()
		if err != nil {
			return fmt.Errorf("getCount was failed: %w", err)
		}

		if count > 0 {
			err = db.readAlarm()
			if err != nil {
				return fmt.Errorf("readAlarm was failed: %w", err)
			}

			toOpc <- &WriteControlMessage{
				NodeId: "ns=2;s=Root.Control",
				Value:  0,
			}
		}

		select {
		case <-ctx.Done():
			return errors.New("other goroutine was ended")
		case err := <-messageLoopErr:
			return err
		case <-time.After(500 * time.Millisecond):
		}
	}
}
