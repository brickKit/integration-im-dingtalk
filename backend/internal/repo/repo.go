// Package repo 是 integration-im-dingtalk 的数据访问层：钉钉 access_token
// 缓存、手机号→userid 缓存、投递尝试记录。只有本组件自己 schema 里的三张表
// 与标准的 Outbox / Inbox，不读任何别的组件的表。
package repo

import (
	"database/sql"
	"errors"
	"fmt"
)

var ErrNotFound = errors.New("not found")
var ErrInvalidArgument = errors.New("参数不合法")

type Repo struct {
	db     *sql.DB
	role   string
	schema string
}

func New(db *sql.DB, role, schema string) *Repo {
	return &Repo{db: db, role: role, schema: schema}
}

func wrap(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", action, err)
}
