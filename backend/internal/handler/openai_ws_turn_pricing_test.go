package handler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestOpenAIWSTurnPricingZeroValue 钉死 WS turn 定价的零值语义：连接建立后、
// 首个 turn 被成功接收前必须保持零值，不能预填建连时刻。
//
// 若用建连时刻初始化，会把长连接的后续 turn 钉死在建连时的高峰因子，客户端
// 峰前建连并保活即可让后续 turn 沿用旧价格。
func TestOpenAIWSTurnPricingZeroValue(t *testing.T) {
	var p openAIWSTurnPricing
	require.True(t, p.current().IsZero(),
		"首个 turn 起始回调前必须保持零值")
}

// TestOpenAIWSTurnPricingFreezePerTurn 钉死每个 turn 的 BeforeTurn 都会覆盖
// 上一个 turn 的定价时刻：长连接跨峰谷时后续 turn 不得沿用旧时刻。
func TestOpenAIWSTurnPricingFreezePerTurn(t *testing.T) {
	var p openAIWSTurnPricing
	turn1 := time.Now().Add(-time.Hour)
	turn2 := time.Now()

	p.freeze(turn1)
	require.Equal(t, turn1, p.current())

	p.freeze(turn2)
	require.Equal(t, turn2, p.current(), "后续 turn 必须使用自己的定价时刻")
}
