// Command integration-im-dingtalk runs the DingTalk channel member alone (serve, migrate); the shells compile in
// dingtalk.Definition().
package main

import (
	"github.com/brickKit/be-sdk-go/v3/launcher"
	"github.com/brickKit/integration-im-dingtalk/v3/dingtalk"
)

func main() { launcher.Main(dingtalk.Definition()) }
