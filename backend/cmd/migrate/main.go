package main

import (
	"github.com/brickKit/be-sdk-go/migrate"
	"github.com/brickKit/integration-im-dingtalk/v2/migrations"
)

func main() { migrate.Main(migrations.FS) }
