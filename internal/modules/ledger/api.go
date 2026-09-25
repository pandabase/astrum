package ledger

import (
	"github.com/pandabase/astrum/internal/kernel/typeid"
)

type (
	ledgerPrefix      struct{}
	accountPrefix     struct{}
	transactionPrefix struct{}
	holdPrefix        struct{}
	schedulePrefix    struct{}
	statementPrefix   struct{}
	categoryPrefix    struct{}
	monitorPrefix     struct{}
	bulkPrefix        struct{}
	settlementPrefix  struct{}
)

func (ledgerPrefix) Prefix() string      { return "ldg" }
func (accountPrefix) Prefix() string     { return "acct" }
func (transactionPrefix) Prefix() string { return "txn" }
func (holdPrefix) Prefix() string        { return "hold" }
func (schedulePrefix) Prefix() string    { return "sched" }
func (statementPrefix) Prefix() string   { return "stmt" }
func (categoryPrefix) Prefix() string    { return "cat" }
func (monitorPrefix) Prefix() string     { return "bm" }
func (bulkPrefix) Prefix() string        { return "blk" }
func (settlementPrefix) Prefix() string  { return "stl" }

type (
	ledgerID      = typeid.ID[ledgerPrefix]
	accountID     = typeid.ID[accountPrefix]
	transactionID = typeid.ID[transactionPrefix]
	holdID        = typeid.ID[holdPrefix]
	scheduleID    = typeid.ID[schedulePrefix]
	statementID   = typeid.ID[statementPrefix]
	categoryID    = typeid.ID[categoryPrefix]
	monitorID     = typeid.ID[monitorPrefix]
	bulkID        = typeid.ID[bulkPrefix]
	settlementID  = typeid.ID[settlementPrefix]
)

type deleted struct {
	Object  string `json:"object"`
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}
