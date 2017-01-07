// Copyright 2016 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"database/sql"
	"fmt"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/ngaut/log"
	. "github.com/pingcap/check"
	gmysql "github.com/siddontang/go-mysql/mysql"
	"github.com/siddontang/go-mysql/replication"
	"golang.org/x/net/context"
)

var _ = Suite(&testSyncerSuite{})

func TestSuite(t *testing.T) {
	TestingT(t)
}

type testSyncerSuite struct {
	db       *sql.DB
	syncer   *replication.BinlogSyncer
	streamer *replication.BinlogStreamer
	cfg      *Config
}

func (s *testSyncerSuite) SetUpSuite(c *C) {
	s.cfg = &Config{
		From: DBConfig{
			Host:     "127.0.0.1",
			User:     "root",
			Password: "",
			Port:     3306,
		},
		ServerID: 101,
	}

	var err error
	dbAddr := fmt.Sprintf("%s:%s@tcp(%s:%d)/?charset=utf8", s.cfg.From.User, s.cfg.From.Password, s.cfg.From.Host, s.cfg.From.Port)
	s.db, err = sql.Open("mysql", dbAddr)
	if err != nil {
		log.Fatal(err)
	}

	s.syncer = replication.NewBinlogSyncer(&replication.BinlogSyncerConfig{
		ServerID: uint32(s.cfg.ServerID),
		Flavor:   "mysql",
		Host:     s.cfg.From.Host,
		Port:     uint16(s.cfg.From.Port),
		User:     s.cfg.From.User,
		Password: s.cfg.From.Password,
	})
	s.resetMaster()
	s.streamer, err = s.syncer.StartSync(gmysql.Position{Name: "", Pos: 4})
	if err != nil {
		log.Fatal(err)
	}

	s.db.Exec("SET GLOBAL binlog_format = 'ROW';")
}

func (s *testSyncerSuite) TearDownSuite(c *C) {
	s.db.Close()
}

func (s *testSyncerSuite) resetMaster() {
	s.db.Exec("reset master")
}

func (s *testSyncerSuite) TestSelectDB(c *C) {
	s.cfg.DoDB = []string{"~^b.*", "s1", "stest"}
	sqls := []string{
		"create database s1",
		"drop database s1",
		"create database s2",
		"drop database s2",
		"create database btest",
		"drop database btest",
		"create database b1",
		"drop database b1",
		"create database stest",
		"drop database stest",
		"create database st",
		"drop database st",
	}
	res := []bool{false, false, true, true, false, false, false, false, false, false, true, true}

	for _, sql := range sqls {
		s.db.Exec(sql)
	}

	syncer := NewSyncer(s.cfg)
	syncer.genRegexMap()
	i := 0
	for {
		if i >= len(sqls) {
			break
		}

		e, err := s.streamer.GetEvent(context.Background())
		if err != nil {
			log.Fatal(err)
		}
		ev, ok := e.Event.(*replication.QueryEvent)
		if !ok {
			continue
		}
		sql := string(ev.Query)
		if syncer.skipQueryEvent(sql, string(ev.Schema)) {
			continue
		}
		r := syncer.skipQueryDDL(sql, string(ev.Schema))
		c.Assert(r, Equals, res[i])
		i++
	}
}

func (s *testSyncerSuite) TestSelectTable(c *C) {
	s.cfg.DoDB = []string{"t2"}
	s.cfg.DoTable = []TableName{
		{Schema: "stest", Name: "log"},
		{Schema: "stest", Name: "~^t.*"},
	}
	sqls := []string{
		"create database s1",
		"create table s1.log(id int)",
		"drop database s1",

		"create table mysql.test(id int)",
		"drop table mysql.test",
		"create database stest",
		"create table stest.log(id int)",
		"create table stest.t(id int)",
		"create table stest.log2(id int)",
		"insert into stest.t(id) values (10)",
		"insert into stest.log(id) values (10)",
		"insert into stest.log2(id) values (10)",
		"drop table stest.log,stest.t,stest.log2",
		"drop database stest",

		"create database t2",
		"create table t2.log(id int)",
		"create table t2.log1(id int)",
		"drop table t2.log",
		"drop database t2",
	}
	res := [][]bool{
		{true},
		{true},
		{true},

		{true},
		{true},
		{false},
		{false},
		{false},
		{true},
		{false},
		{false},
		{true},
		{false, false, true},
		{false},

		{false},
		{false},
		{false},
		{false},
		{false},
	}

	for _, sql := range sqls {
		s.db.Exec(sql)
	}

	syncer := NewSyncer(s.cfg)
	syncer.genRegexMap()
	i := 0
	for {
		if i >= len(sqls) {
			break
		}

		e, err := s.streamer.GetEvent(context.Background())
		if err != nil {
			log.Fatal(err)
		}
		switch ev := e.Event.(type) {
		case *replication.QueryEvent:
			query := string(ev.Query)
			if syncer.skipQueryEvent(query, string(ev.Schema)) {
				continue
			}

			querys, ok, err := resolveDDLSQL(query)
			if !ok {
				continue
			}
			if err != nil {
				log.Fatalf("ResolveDDlSQL failed %v", err)
			}
			for j, q := range querys {
				r := syncer.skipQueryDDL(q, string(ev.Schema))
				c.Assert(r, Equals, res[i][j])
			}
		case *replication.RowsEvent:
			r := syncer.skipRowEvent(string(ev.Table.Schema), string(ev.Table.Table))
			c.Assert(r, Equals, res[i][0])

		default:
			continue
		}

		i++

	}
}

func (s *testSyncerSuite) TestIgnoreDB(c *C) {
	s.cfg.IgnoreDB = []string{"~^b.*", "s1", "stest"}
	sqls := []string{
		"create database s1",
		"drop database s1",
		"create database s2",
		"drop database s2",
		"create database btest",
		"drop database btest",
		"create database b1",
		"drop database b1",
		"create database stest",
		"drop database stest",
		"create database st",
		"drop database st",
	}
	res := []bool{true, true, false, false, true, true, true, true, true, true, false, false}

	for _, sql := range sqls {
		s.db.Exec(sql)
	}

	syncer := NewSyncer(s.cfg)
	syncer.genRegexMap()
	i := 0
	for {
		if i >= len(sqls) {
			break
		}

		e, err := s.streamer.GetEvent(context.Background())
		if err != nil {
			log.Fatal(err)
		}
		ev, ok := e.Event.(*replication.QueryEvent)
		if !ok {
			continue
		}
		sql := string(ev.Query)
		if syncer.skipQueryEvent(sql, string(ev.Schema)) {
			continue
		}
		r := syncer.skipQueryDDL(sql, string(ev.Schema))
		c.Assert(r, Equals, res[i])
		i++
	}
}

func (s *testSyncerSuite) TestIgnoreTable(c *C) {
	s.cfg.IgnoreDB = []string{"t2"}
	s.cfg.IgnoreTable = []TableName{
		{Schema: "stest", Name: "log"},
		{Schema: "stest", Name: "~^t.*"},
	}
	sqls := []string{
		"create database s1",
		"create table s1.log(id int)",
		"drop database s1",

		"create table mysql.test(id int)",
		"drop table mysql.test",
		"create database stest",
		"create table stest.log(id int)",
		"create table stest.t(id int)",
		"create table stest.log2(id int)",
		"insert into stest.t(id) values (10)",
		"insert into stest.log(id) values (10)",
		"insert into stest.log2(id) values (10)",
		"drop table stest.log,stest.t,stest.log2",
		"drop database stest",

		"create database t2",
		"create table t2.log(id int)",
		"create table t2.log1(id int)",
		"drop table t2.log",
		"drop database t2",
	}
	res := [][]bool{
		{false},
		{false},
		{false},

		{true},
		{true},
		{true},
		{true},
		{true},
		{false},
		{true},
		{true},
		{false},
		{true, true, false},
		{true},

		{true},
		{true},
		{true},
		{true},
		{true},
	}

	for _, sql := range sqls {
		s.db.Exec(sql)
	}

	syncer := NewSyncer(s.cfg)
	syncer.genRegexMap()
	i := 0
	for {
		if i >= len(sqls) {
			break
		}

		e, err := s.streamer.GetEvent(context.Background())
		if err != nil {
			log.Fatal(err)
		}
		switch ev := e.Event.(type) {
		case *replication.QueryEvent:
			query := string(ev.Query)
			if syncer.skipQueryEvent(query, string(ev.Schema)) {
				continue
			}

			querys, ok, err := resolveDDLSQL(query)
			if !ok {
				continue
			}
			if err != nil {
				log.Fatalf("ResolveDDlSQL failed %v", err)
			}
			for j, q := range querys {
				r := syncer.skipQueryDDL(q, string(ev.Schema))
				c.Assert(r, Equals, res[i][j])
			}
		case *replication.RowsEvent:
			r := syncer.skipRowEvent(string(ev.Table.Schema), string(ev.Table.Table))
			c.Assert(r, Equals, res[i][0])

		default:
			continue
		}

		i++

	}
}
