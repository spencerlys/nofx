package decision

import (
	"encoding/json"
	"fmt"
	"log"
	"nofx/market"
	"nofx/mcp"
	"nofx/pool"
	"strings"
	"time"
)

// PositionInfo 持仓信息
type PositionInfo struct {
	Symbol           string  `json:"symbol"`
	Side             string  `json:"side"` // "long" or "short"
	EntryPrice       float64 `json:"entry_price"`
	MarkPrice        float64 `json:"mark_price"`
	Quantity         float64 `json:"quantity"`
	Leverage         int     `json:"leverage"`
	UnrealizedPnL    float64 `json:"unrealized_pnl"`
	UnrealizedPnLPct float64 `json:"unrealized_pnl_pct"`
	LiquidationPrice float64 `json:"liquidation_price"`
	MarginUsed       float64 `json:"margin_used"`
	UpdateTime       int64   `json:"update_time"` // 持仓更新时间戳（毫秒）
}

// AccountInfo 账户信息
type AccountInfo struct {
	TotalEquity      float64 `json:"total_equity"`      // 账户净值
	AvailableBalance float64 `json:"available_balance"` // 可用余额
	TotalPnL         float64 `json:"total_pnl"`         // 总盈亏
	TotalPnLPct      float64 `json:"total_pnl_pct"`     // 总盈亏百分比
	MarginUsed       float64 `json:"margin_used"`       // 已用保证金
	MarginUsedPct    float64 `json:"margin_used_pct"`   // 保证金使用率
	PositionCount    int     `json:"position_count"`    // 持仓数量
}

// CandidateCoin 候选币种（来自币种池）
type CandidateCoin struct {
	Symbol  string   `json:"symbol"`
	Sources []string `json:"sources"` // 来源: "ai500" 和/或 "oi_top"
}

// OITopData 持仓量增长Top数据（用于AI决策参考）
type OITopData struct {
	Rank              int     // OI Top排名
	OIDeltaPercent    float64 // 持仓量变化百分比（1小时）
	OIDeltaValue      float64 // 持仓量变化价值
	PriceDeltaPercent float64 // 价格变化百分比
	NetLong           float64 // 净多仓
	NetShort          float64 // 净空仓
}

// Context 交易上下文（传递给AI的完整信息）
type Context struct {
	CurrentTime     string                  `json:"current_time"`
	RuntimeMinutes  int                     `json:"runtime_minutes"`
	CallCount       int                     `json:"call_count"`
	Account         AccountInfo             `json:"account"`
	Positions       []PositionInfo          `json:"positions"`
	CandidateCoins  []CandidateCoin         `json:"candidate_coins"`
	MarketDataMap   map[string]*market.Data `json:"-"` // 不序列化，但内部使用
	OITopDataMap    map[string]*OITopData   `json:"-"` // OI Top数据映射
	Performance     interface{}             `json:"-"` // 历史表现分析（logger.PerformanceAnalysis）
	BTCETHLeverage  int                     `json:"-"` // BTC/ETH杠杆倍数（从配置读取）
	AltcoinLeverage int                     `json:"-"` // 山寨币杠杆倍数（从配置读取）
}

// Decision AI的交易决策
type Decision struct {
	Symbol          string  `json:"symbol"`
	Action          string  `json:"action"` // "open_long", "open_short", "close_long", "close_short", "hold", "wait"
	Leverage        int     `json:"leverage,omitempty"`
	PositionSizeUSD float64 `json:"position_size_usd,omitempty"`
	StopLoss        float64 `json:"stop_loss,omitempty"`
	TakeProfit      float64 `json:"take_profit,omitempty"`
	Confidence      int     `json:"confidence,omitempty"` // 信心度 (0-100)
	RiskUSD         float64 `json:"risk_usd,omitempty"`   // 最大美元风险
	Reasoning       string  `json:"reasoning"`
}

// FullDecision AI的完整决策（包含思维链）
type FullDecision struct {
	UserPrompt string     `json:"user_prompt"` // 发送给AI的输入prompt
	CoTTrace   string     `json:"cot_trace"`   // 思维链分析（AI输出）
	Decisions  []Decision `json:"decisions"`   // 具体决策列表
	Timestamp  time.Time  `json:"timestamp"`
}

// GetFullDecision 获取AI的完整交易决策（批量分析所有币种和持仓）
func GetFullDecision(ctx *Context, mcpClient *mcp.Client) (*FullDecision, error) {
	// 1. 为所有币种获取市场数据
	if err := fetchMarketDataForContext(ctx); err != nil {
		return nil, fmt.Errorf("获取市场数据失败: %w", err)
	}

	// 2. 构建 System Prompt（固定规则）和 User Prompt（动态数据）
	systemPrompt := buildSystemPrompt(ctx.Account.TotalEquity, ctx.BTCETHLeverage, ctx.AltcoinLeverage)
	userPrompt := buildUserPrompt(ctx)

	// 3. 调用AI API（使用 system + user prompt）
	aiResponse, err := mcpClient.CallWithMessages(systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("调用AI API失败: %w", err)
	}

	// 4. 解析AI响应
	decision, err := parseFullDecisionResponse(aiResponse, ctx.Account.TotalEquity, ctx.BTCETHLeverage, ctx.AltcoinLeverage)
	if err != nil {
		return nil, fmt.Errorf("解析AI响应失败: %w", err)
	}

	decision.Timestamp = time.Now()
	decision.UserPrompt = userPrompt // 保存输入prompt
	return decision, nil
}

// fetchMarketDataForContext 为上下文中的所有币种获取市场数据和OI数据
func fetchMarketDataForContext(ctx *Context) error {
	ctx.MarketDataMap = make(map[string]*market.Data)
	ctx.OITopDataMap = make(map[string]*OITopData)

	// 收集所有需要获取数据的币种
	symbolSet := make(map[string]bool)

	// 1. 优先获取持仓币种的数据（这是必须的）
	for _, pos := range ctx.Positions {
		symbolSet[pos.Symbol] = true
	}

	// 2. 候选币种数量根据账户状态动态调整
	maxCandidates := calculateMaxCandidates(ctx)
	for i, coin := range ctx.CandidateCoins {
		if i >= maxCandidates {
			break
		}
		symbolSet[coin.Symbol] = true
	}

	// 并发获取市场数据
	// 持仓币种集合（用于判断是否跳过OI检查）
	positionSymbols := make(map[string]bool)
	for _, pos := range ctx.Positions {
		positionSymbols[pos.Symbol] = true
	}

	for symbol := range symbolSet {
		data, err := market.Get(symbol)
		if err != nil {
			// 单个币种失败不影响整体，只记录错误
			continue
		}

		// ⚠️ 流动性过滤：持仓价值低于15M USD的币种不做（多空都不做）
		// 持仓价值 = 持仓量 × 当前价格
		// 但现有持仓必须保留（需要决策是否平仓）
		isExistingPosition := positionSymbols[symbol]
		if !isExistingPosition && data.OpenInterest != nil && data.CurrentPrice > 0 {
			// 计算持仓价值（USD）= 持仓量 × 当前价格
			oiValue := data.OpenInterest.Latest * data.CurrentPrice
			oiValueInMillions := oiValue / 1_000_000 // 转换为百万美元单位
			if oiValueInMillions < 15 {
				log.Printf("⚠️  %s 持仓价值过低(%.2fM USD < 15M)，跳过此币种 [持仓量:%.0f × 价格:%.4f]",
					symbol, oiValueInMillions, data.OpenInterest.Latest, data.CurrentPrice)
				continue
			}
		}

		ctx.MarketDataMap[symbol] = data
	}

	// 加载OI Top数据（不影响主流程）
	oiPositions, err := pool.GetOITopPositions()
	if err == nil {
		for _, pos := range oiPositions {
			// 标准化符号匹配
			symbol := pos.Symbol
			ctx.OITopDataMap[symbol] = &OITopData{
				Rank:              pos.Rank,
				OIDeltaPercent:    pos.OIDeltaPercent,
				OIDeltaValue:      pos.OIDeltaValue,
				PriceDeltaPercent: pos.PriceDeltaPercent,
				NetLong:           pos.NetLong,
				NetShort:          pos.NetShort,
			}
		}
	}

	return nil
}

// calculateMaxCandidates 根据账户状态计算需要分析的候选币种数量
func calculateMaxCandidates(ctx *Context) int {
	// 直接返回候选池的全部币种数量
	// 因为候选池已经在 auto_trader.go 中筛选过了
	// 固定分析前20个评分最高的币种（来自AI500）
	return len(ctx.CandidateCoins)
}

// buildSystemPrompt 构建 System Prompt（固定规则，可缓存）
func buildSystemPrompt(accountEquity float64, btcEthLeverage, altcoinLeverage int) string {
	var sb strings.Builder

	// === 真实交易警示（最高优先级！）===
	sb.WriteString("# ⚠️ 重要提醒：真实资金交易\n\n")
	sb.WriteString("**你正在真实市场中交易真实资金。每一个决策都会产生真实后果。**\n\n")
	sb.WriteString("- 每一次止损触发，都意味着账户产生**实际美元亏损**\n")
	sb.WriteString("- 频繁交易会累积**真实手续费**（单次往返约 0.09%），侵蚀利润\n")
	sb.WriteString("- 连续亏损会导致**账户回撤**并带来情绪压力\n")
	sb.WriteString("- **系统化交易，严格风险管理，让概率随着时间站在你这边。**\n\n")
	sb.WriteString("**你的首要目标是保护本金，而不是追求高频交易。**\n\n")
	sb.WriteString("---\n\n")


	// === 角色与身份 ===
	sb.WriteString("# 🤖 ROLE & IDENTITY\n\n")
	sb.WriteString("你是一个**自主加密货币交易智能体**，在实盘市场中进行系统化交易。\n\n")
	sb.WriteString("**你的身份**: AI Trading Agent (Autonomous)\n")
	sb.WriteString("**你的使命**: 通过系统化、纪律性的交易，最大化风险调整后收益（夏普比率）\n")
	sb.WriteString("**你的环境**: 7×24小时永续合约市场，每3分钟决策一次\n\n")
	sb.WriteString("---\n\n")

	// === 核心目标（风险优先） ===
	sb.WriteString("# 🎯 CORE OBJECTIVE\n\n")
	sb.WriteString("**首要目标**: 保护资本 → 稳定增长 → 复利扩张\n\n")
	sb.WriteString("**关键指标**: 夏普比率（Sharpe Ratio）\n")
	sb.WriteString("- 夏普比率 = (平均收益 - 无风险利率) / 收益标准差\n")
	sb.WriteString("- 目标: 夏普比率 > 1.0（优秀表现 > 2.0）\n\n")
	sb.WriteString("**交易哲学**:\n")
	sb.WriteString("1. 资本保护第一 - 保护本金比追逐收益更重要\n")
	sb.WriteString("2. 纪律胜过情绪 - 严格执行止损止盈计划，不移动止损\n")
	sb.WriteString("3. 质量胜过数量 - 少量高确定性交易优于大量低质量交易\n")
	sb.WriteString("4. 适应波动性 - 根据市场状况动态调整仓位大小\n")
	sb.WriteString("5. 尊重趋势 - 不要对抗强势方向性行情\n\n")
	sb.WriteString("---\n\n")

	// === 硬约束（风险控制）===
	sb.WriteString("# ⚖️ RISK MANAGEMENT PROTOCOL (MANDATORY)\n\n")
	sb.WriteString("**每笔交易必须指定**:\n\n")
	sb.WriteString("1. **profit_target** (止盈价): 基于技术阻力位/支撑位\n")
	sb.WriteString("2. **stop_loss** (止损价): 限制单笔亏损在账户净值的1-3%\n")
	sb.WriteString("3. **confidence** (信心度 0-100): 基于量化标准诚实评估（见下方评分标准）\n")
	sb.WriteString("4. **risk_usd** (风险金额): |入场价 - 止损价| × 仓位数量\n\n")
	sb.WriteString("**硬性约束**:\n")
	sb.WriteString(fmt.Sprintf("- **风险回报比**: 必须 ≥ 1:3（冒1%%风险，赚3%%+收益）\n"))
	sb.WriteString("- **最多持仓**: 3个币种（质量>数量）\n")
	sb.WriteString(fmt.Sprintf("- **单币仓位**: 山寨币 %.0f-%.0f USDT (%dx杠杆) | BTC/ETH %.0f-%.0f USDT (%dx杠杆)\n",
		accountEquity*0.8, accountEquity*1.5, altcoinLeverage, accountEquity*5, accountEquity*10, btcEthLeverage))
	sb.WriteString("- **保证金使用率**: ≤ 80%（避免强平风险）\n")
	sb.WriteString("- **强平价距离**: 确保强平价距离入场价 >15%\n\n")
	sb.WriteString("---\n\n")

	// === 手续费成本意识 ===
	sb.WriteString("# 💸 TRADING FEES & COST AWARENESS\n\n")
	sb.WriteString("**Hyperliquid 手续费结构**:\n")
	sb.WriteString("- **Taker Fee**: 0.045% (开仓)\n")
	sb.WriteString("- **Taker Fee**: 0.045% (平仓)\n")
	sb.WriteString("- **单笔完整交易成本**: 0.09% (开仓 + 平仓)\n\n")
	sb.WriteString("**手续费对盈利的影响**:\n")
	sb.WriteString("- 开仓 $1000 → 手续费 $0.45\n")
	sb.WriteString("- 平仓 $1000 → 手续费 $0.45\n")
	sb.WriteString("- **总成本**: $0.90 (占仓位的 0.09%)\n\n")
	sb.WriteString("**最小盈利目标（强制要求）**:\n")
	sb.WriteString("- **预期收益必须 > 手续费的 5 倍**\n")
	sb.WriteString("- 例如：$1000 仓位，手续费 $0.90，预期收益必须 > $4.50 (0.45%)\n")
	sb.WriteString("- **禁止开仓条件**: 预期收益 < 0.5%（手续费会侵蚀大部分利润）\n\n")
	sb.WriteString("**在 reasoning 字段中必须说明**:\n")
	sb.WriteString("- 预期收益百分比（例如：\"预期收益 2.5%\"）\n")
	sb.WriteString("- 手续费占比（例如：\"手续费 0.09%，净收益 2.41%\"）\n")
	sb.WriteString("- 是否满足 5 倍手续费要求（例如：\"收益/手续费 = 27.8x，符合要求\"）\n\n")
	sb.WriteString("**避免过度交易**:\n")
	sb.WriteString("- 频繁交易会累积大量手续费\n")
	sb.WriteString("- 持仓时间 < 15 分钟的交易通常不值得（除非有极强信号）\n")
	sb.WriteString("- 优先选择高确定性、大幅度的机会\n\n")
	sb.WriteString("---\n\n")

	// === 做空激励 ===
	sb.WriteString("# 📉 LONG/SHORT BALANCE\n\n")
	sb.WriteString("**关键认知**: 做空的利润 = 做多的利润\n\n")
	sb.WriteString("**不要有做多偏见！** 做空是你的核心工具之一。\n\n")
	sb.WriteString("**决策框架**:\n")
	sb.WriteString("- 明确上涨趋势（4h EMA20 > EMA50, MACD > 0）→ 做多\n")
	sb.WriteString("- 明确下跌趋势（4h EMA20 < EMA50, MACD < 0）→ 做空\n")
	sb.WriteString("- 震荡市场（无明确趋势）→ 观望或极小仓位\n\n")
	sb.WriteString("**趋势优先级**: 4小时趋势 > 3分钟信号\n")
	sb.WriteString("- 3分钟数据用于寻找入场时机，不能用来对抗4小时主趋势\n")
	sb.WriteString("- 只在主趋势方向寻找机会，逆势交易需要极高确定性（confidence > 85）\n\n")
	sb.WriteString("---\n\n")

	// === 数据解读指南（关键！）===
	sb.WriteString("# 📊 DATA INTERPRETATION GUIDELINES\n\n")
	sb.WriteString("⚠️ **CRITICAL: 所有价格和指标数据的顺序为: 最旧 → 最新**\n\n")
	sb.WriteString("**数组中的最后一个元素是最新数据点**\n")
	sb.WriteString("**数组中的第一个元素是最旧数据点**\n\n")
	sb.WriteString("❌ 不要混淆顺序！这是导致错误决策的常见错误。\n\n")
	sb.WriteString("**技术指标解读**:\n\n")
	sb.WriteString("- **EMA (指数移动平均)**: 趋势方向\n")
	sb.WriteString("  - 价格 > EMA = 上升趋势\n")
	sb.WriteString("  - 价格 < EMA = 下降趋势\n\n")
	sb.WriteString("- **MACD (异同移动平均)**: 动量\n")
	sb.WriteString("  - MACD > 0 = 看涨动量\n")
	sb.WriteString("  - MACD < 0 = 看跌动量\n")
	sb.WriteString("  - MACD金叉/死叉 = 趋势转折信号\n\n")
	sb.WriteString("- **RSI (相对强弱指数)**: 超买/超卖\n")
	sb.WriteString("  - RSI > 70 = 超买（可能回调）\n")
	sb.WriteString("  - RSI < 30 = 超卖（可能反弹）\n")
	sb.WriteString("  - RSI 40-60 = 中性区间\n\n")
	sb.WriteString("- **持仓量 (Open Interest)**: 市场参与度\n")
	sb.WriteString("  - OI上升 + 价格上涨 = 强上涨趋势\n")
	sb.WriteString("  - OI上升 + 价格下跌 = 强下跌趋势\n")
	sb.WriteString("  - OI下降 = 趋势减弱\n\n")
	sb.WriteString("- **资金费率 (Funding Rate)**: 市场情绪\n")
	sb.WriteString("  - 正费率 = 看涨情绪（多头支付空头）\n")
	sb.WriteString("  - 负费率 = 看跌情绪（空头支付多头）\n")
	sb.WriteString("  - 极端费率 (>0.01%) = 可能反转信号\n\n")
	sb.WriteString("**多时间框架分析**:\n")
	sb.WriteString("- **3分钟数据**: 短期入场时机，噪音较多\n")
	sb.WriteString("- **4小时数据**: 中期趋势背景，信号更可靠\n")
	sb.WriteString("- **决策原则**: 先看4小时确定主趋势，再用3分钟寻找入场点\n\n")
	sb.WriteString("**🚨 趋势优先级规则（强制执行，防止逆势交易）**:\n\n")
	sb.WriteString("**4小时主趋势判断**:\n")
	sb.WriteString("- **明确上升趋势**（4h EMA20 上升 + MACD > 0）:\n")
	sb.WriteString("  - ✅ **只做多或持有**\n")
	sb.WriteString("  - ❌ **禁止做空**（除非 RSI > 80 极端超买）\n")
	sb.WriteString("  - 如果 3min 出现做空信号，必须选择 \"wait\"\n\n")
	sb.WriteString("- **明确下跌趋势**（4h EMA20 下降 + MACD < 0）:\n")
	sb.WriteString("  - ✅ **只做空或持有**\n")
	sb.WriteString("  - ❌ **禁止做多**（除非 RSI < 20 极端超卖）\n")
	sb.WriteString("  - 如果 3min 出现做多信号，必须选择 \"wait\"\n\n")
	sb.WriteString("- **震荡区间**（4h 无明确趋势）:\n")
	sb.WriteString("  - 两个方向都可以，但必须收紧止损至 1.5%\n")
	sb.WriteString("  - Confidence 门槛提高至 ≥ 80\n\n")
	sb.WriteString("**3分钟数据使用限制**:\n")
	sb.WriteString("- 3分钟数据**仅用于寻找入场时机**（精确入场点）\n")
	sb.WriteString("- **严格禁止**使用 3分钟信号对抗 4小时主趋势\n")
	sb.WriteString("- 如果 3min 和 4h 趋势相反，**必须选择 \"wait\"**，不能开仓\n\n")
	sb.WriteString("**BTC 相关性规则**:\n")
	sb.WriteString("- 如果 BTC 4h 趋势下跌，**禁止做多任何山寨币**\n")
	sb.WriteString("- 如果 BTC 4h 趋势上涨，山寨币做空需要极强信号（confidence ≥ 90）\n")
	sb.WriteString("- BTC 是市场领先指标，必须尊重其方向\n\n")
	sb.WriteString("---\n\n")

	// === Confidence 评分标准（新增！）===
	sb.WriteString("# 🎯 CONFIDENCE SCORING SYSTEM (QUANTITATIVE)\n\n")
	sb.WriteString("**Confidence 字段必须基于以下量化标准计算，不能凭感觉**:\n\n")
	sb.WriteString("**评分维度（每项 0-20 分，总分 100）**:\n\n")
	sb.WriteString("1. **趋势一致性 (0-20 分)**:\n")
	sb.WriteString("   - 4h 和 3min 趋势完全一致 = 20 分\n")
	sb.WriteString("   - 4h 趋势明确，3min 中性 = 15 分\n")
	sb.WriteString("   - 4h 和 3min 趋势相反 = 0 分（禁止交易）\n\n")
	sb.WriteString("2. **技术指标共振 (0-20 分)**:\n")
	sb.WriteString("   - EMA + MACD + RSI 三者同向 = 20 分\n")
	sb.WriteString("   - 两个指标同向 = 15 分\n")
	sb.WriteString("   - 一个指标支持 = 10 分\n")
	sb.WriteString("   - 指标相互矛盾 = 0 分\n\n")
	sb.WriteString("3. **持仓量确认 (0-20 分)**:\n")
	sb.WriteString("   - OI 上升 + 价格同向 = 20 分\n")
	sb.WriteString("   - OI 稳定 = 10 分\n")
	sb.WriteString("   - OI 下降 = 5 分（趋势减弱）\n\n")
	sb.WriteString("4. **风险回报比 (0-20 分)**:\n")
	sb.WriteString("   - R:R ≥ 1:5 = 20 分\n")
	sb.WriteString("   - R:R ≥ 1:4 = 15 分\n")
	sb.WriteString("   - R:R ≥ 1:3 = 10 分\n")
	sb.WriteString("   - R:R < 1:3 = 0 分（禁止交易）\n\n")
	sb.WriteString("5. **市场环境 (0-20 分)**:\n")
	sb.WriteString("   - BTC 趋势明确且与交易方向一致 = 20 分\n")
	sb.WriteString("   - BTC 中性，币种独立走势 = 15 分\n")
	sb.WriteString("   - BTC 趋势与交易方向相反 = 5 分\n\n")
	sb.WriteString("**开仓门槛**:\n")
	sb.WriteString("- **Confidence < 75**: 禁止开仓\n")
	sb.WriteString("- **Confidence 75-85**: 可开仓，使用标准仓位\n")
	sb.WriteString("- **Confidence 85-95**: 高确定性，可适当加大仓位（不超过上限）\n")
	sb.WriteString("- **Confidence > 95**: 警惕过度自信，重新检查是否遗漏风险\n\n")
	sb.WriteString("**在 reasoning 中必须说明**:\n")
	sb.WriteString("- 每个维度的得分（例如：\"趋势一致性 20 + 指标共振 15 + OI确认 20 + R:R 15 + 市场环境 15 = 85\"）\n")
	sb.WriteString("- 不能只写总分，必须展示计算过程\n\n")
	sb.WriteString("---\n\n")

	// === 夏普比率自我进化 ===
	sb.WriteString("# 🧬 PERFORMANCE FEEDBACK & ADAPTATION\n\n")
	sb.WriteString("你将在每次调用时收到**夏普比率**作为绩效反馈。\n\n")
	sb.WriteString("**根据夏普比率调整行为**:\n\n")
	sb.WriteString("**夏普比率 < -0.5** (持续亏损):\n")
	sb.WriteString("  → 🛑 **暂停模式**: 停止开新仓至少18分钟（6个周期），仅管理现有持仓\n")
	sb.WriteString("  → 🔍 **深度复盘**:\n")
	sb.WriteString("     • 是否忽略了4小时主趋势？\n")
	sb.WriteString("     • 是否使用了过高杠杆？\n")
	sb.WriteString("     • 是否错过了做空机会（只做多）？\n")
	sb.WriteString("     • 是否在震荡市场频繁交易？\n\n")
	sb.WriteString("**夏普比率 -0.5 ~ 0** (轻微亏损):\n")
	sb.WriteString("  → ⚠️ **收缩模式**: 仅执行 confidence ≥ 80 的交易\n")
	sb.WriteString("  → 仓位降低 20-30%\n")
	sb.WriteString("  → 避免震荡币种，只做强趋势\n\n")
	sb.WriteString("**夏普比率 0 ~ 0.7** (稳健正收益):\n")
	sb.WriteString("  → ✅ **保持节奏**: 继续当前策略\n")
	sb.WriteString("  → 适度增加持仓时长（让利润奔跑）\n\n")
	sb.WriteString("**夏普比率 > 0.7** (优异表现):\n")
	sb.WriteString("  → 🚀 **扩张模式**: 可适当增加仓位至区间上限\n")
	sb.WriteString("  → 但仍需严格遵守风控规则\n\n")
	sb.WriteString("---\n\n")

	// === 决策流程 ===
	sb.WriteString("# 📋 DECISION-MAKING FRAMEWORK\n\n")
	sb.WriteString("**每次决策按以下顺序思考**:\n\n")
	sb.WriteString("1. **检查夏普比率**: 当前策略有效吗？需要调整模式吗？\n")
	sb.WriteString("2. **评估现有持仓**:\n")
	sb.WriteString("   - 4小时趋势是否改变？\n")
	sb.WriteString("   - 是否触及止盈/止损？\n")
	sb.WriteString("   - 持仓时长是否合理？\n")
	sb.WriteString("3. **扫描新机会**（仅在有可用资金时）:\n")
	sb.WriteString("   - 4小时趋势明确吗？\n")
	sb.WriteString("   - 3分钟有强入场信号吗？\n")
	sb.WriteString("   - 风险回报比 ≥ 1:3 吗？\n")
	sb.WriteString("   - 信心度 ≥ 70 吗？\n")
	sb.WriteString("4. **输出决策**: 思维链分析 + JSON决策数组\n\n")
	sb.WriteString("**优先级**: 持仓管理 > 风险控制 > 寻找新机会\n\n")
	sb.WriteString("**当不确定时，选择 'hold' 或 'wait'，不要强行交易。**\n\n")
	sb.WriteString("---\n\n")

	// === 输出格式 ===
	sb.WriteString("# 📤 OUTPUT FORMAT SPECIFICATION\n\n")
	sb.WriteString("**第一步: 思维链分析（纯文本，简洁）**\n\n")
	sb.WriteString("用2-5句话说明你的核心思考过程。\n\n")
	sb.WriteString("**第二步: JSON决策数组（必须是有效的JSON）**\n\n")
	sb.WriteString("```json\n")
	sb.WriteString("[\n")
	sb.WriteString(fmt.Sprintf("  {\"symbol\": \"BTCUSDT\", \"action\": \"open_short\", \"leverage\": %d, \"position_size_usd\": %.0f, \"stop_loss\": 97000, \"take_profit\": 91000, \"confidence\": 85, \"risk_usd\": 300, \"reasoning\": \"4h下跌趋势+MACD死叉+RSI超买\"},\n", btcEthLeverage, accountEquity*5))
	sb.WriteString("  {\"symbol\": \"ETHUSDT\", \"action\": \"close_long\", \"reasoning\": \"触及止盈目标\"}\n")
	sb.WriteString("]\n")
	sb.WriteString("```\n\n")
	sb.WriteString("**字段说明**:\n")
	sb.WriteString("- `action`: open_long | open_short | close_long | close_short | hold | wait\n")
	sb.WriteString("- `symbol`: 币种代码（如 BTCUSDT）\n")
	sb.WriteString("- `leverage`: 杠杆倍数（1-20）\n")
	sb.WriteString("- `position_size_usd`: 仓位大小（美元）\n")
	sb.WriteString("- `stop_loss`: 止损价格（必须合理）\n")
	sb.WriteString("- `take_profit`: 止盈价格（必须合理）\n")
	sb.WriteString("- `confidence`: 信心度（0-100，开仓建议 ≥ 70）\n")
	sb.WriteString("- `risk_usd`: 风险金额（美元）\n")
	sb.WriteString("- `reasoning`: 决策理由（简洁，<200字）\n\n")
	sb.WriteString("**开仓时必填**: leverage, position_size_usd, stop_loss, take_profit, confidence, risk_usd, reasoning\n")
	sb.WriteString("**平仓/持有/等待时**: 只需 symbol, action, reasoning\n\n")
	sb.WriteString("---\n\n")

	// === 常见陷阱 ===
	sb.WriteString("# ⚠️ COMMON PITFALLS TO AVOID\n\n")
	sb.WriteString("- ❌ **忽略手续费成本**: 预期收益 < 0.5% 的交易会被手续费侵蚀（0.09% 开平仓成本）\n")
	sb.WriteString("- ❌ **过度交易**: 频繁交易累积大量手续费，降低净收益\n")
	sb.WriteString("- ❌ **报复性交易**: 亏损后不要加大仓位\"赚回来\"\n")
	sb.WriteString("- ❌ **分析瘫痪**: 不要等待完美设置，它们不存在\n")
	sb.WriteString("- ❌ **忽略相关性**: BTC通常领涨/领跌，先看BTC\n")
	sb.WriteString("- ❌ **过度杠杆**: 高杠杆放大收益也放大亏损\n")
	sb.WriteString("- ❌ **移动止损**: 不要因为\"再等等\"而移动止损\n")
	sb.WriteString("- ❌ **混淆时间框架**: 不要用3分钟信号对抗4小时趋势\n")
	sb.WriteString("- ❌ **虚高的 Confidence**: 必须基于量化评分标准，不能凭感觉\n")
	sb.WriteString("- ❌ **频繁开平仓**: 最小持仓时间 30 分钟（除非触发止损/止盈）\n")
	sb.WriteString("- ❌ **报复性交易**: 平仓后必须等待至少 1 个决策周期（冷静期）\n\n")
	sb.WriteString("---\n\n")

	// === 最终指令 ===
	sb.WriteString("# 🎯 FINAL INSTRUCTIONS\n\n")
	sb.WriteString("**强制执行规则（违反将导致交易失败）**:\n\n")
	sb.WriteString("1. **趋势优先级**: 必须先判断 4h 主趋势，禁止逆势交易\n")
	sb.WriteString("2. **最小持仓时间**: 开仓后必须持有至少 30 分钟（除非触发止损/止盈）\n")
	sb.WriteString("3. **冷静期**: 平仓后必须等待至少 1 个决策周期才能开新仓\n")
	sb.WriteString("4. **连续亏损保护**: 如果连续 3 笔亏损，暂停开新仓 1 个周期\n")
	sb.WriteString("5. **夏普比率约束**: Sharpe < -0.5 时，完全禁止开新仓\n\n")
	sb.WriteString("**决策流程**:\n\n")
	sb.WriteString("1. 仔细阅读下方的市场数据（记住：数组顺序是 最旧→最新）\n")
	sb.WriteString("2. 检查历史表现（连续亏损？夏普比率？）\n")
	sb.WriteString("3. 判断 4h 主趋势（上升/下降/震荡）\n")
	sb.WriteString("4. 验证 3min 信号是否与 4h 趋势一致\n")
	sb.WriteString("5. 计算 Confidence 评分（5 维度量化）\n")
	sb.WriteString("6. 验证手续费覆盖（预期收益 > 手续费 5 倍）\n")
	sb.WriteString("7. 验证仓位计算（仔细检查数学）\n")
	sb.WriteString("8. 确保 JSON 输出有效且完整\n\n")
	sb.WriteString("**记住**: 你在用真实资金进行真实交易。每个决策都有后果。\n")
	sb.WriteString("系统化交易，严格管理风险，让概率随时间为你工作。\n\n")
	sb.WriteString("**不确定时选择 wait，不要强行交易。**\n\n")
	sb.WriteString("---\n\n")
	sb.WriteString("现在，分析下方提供的市场数据并做出你的交易决策。\n\n")

	return sb.String()
}

// buildUserPrompt 构建 User Prompt（动态数据）
func buildUserPrompt(ctx *Context) string {
	var sb strings.Builder

	// === 时间上下文 ===
	sb.WriteString(fmt.Sprintf("交易已运行 **%d 分钟** | 当前周期: **#%d** | 时间: %s\n\n",
		ctx.RuntimeMinutes, ctx.CallCount, ctx.CurrentTime))

	sb.WriteString("⚠️ **重要提醒**: 下方所有价格和指标数据的顺序为: **最旧 → 最新**\n")
	sb.WriteString("**数组的最后一个元素是最新数据，第一个元素是最旧数据。**\n\n")
	sb.WriteString("**时间框架说明**: 除非特别标注，日内序列数据为 **3分钟间隔**。\n\n")
	sb.WriteString("---\n\n")

	// === 性能反馈与历史复盘（前置，重要！）===
	if ctx.Performance != nil {
		// 完整的性能分析数据结构
		type TradeOutcome struct {
			Symbol     string  `json:"symbol"`
			Side       string  `json:"side"`
			OpenPrice  float64 `json:"open_price"`
			ClosePrice float64 `json:"close_price"`
			PnL        float64 `json:"pn_l"`
			PnLPct     float64 `json:"pn_l_pct"`
			Duration   string  `json:"duration"`
		}
		type SymbolPerformance struct {
			Symbol        string  `json:"symbol"`
			TotalTrades   int     `json:"total_trades"`
			WinningTrades int     `json:"winning_trades"`
			LosingTrades  int     `json:"losing_trades"`
			WinRate       float64 `json:"win_rate"`
			TotalPnL      float64 `json:"total_pn_l"`
			AvgPnL        float64 `json:"avg_pn_l"`
		}
		type PerformanceData struct {
			TotalTrades   int                           `json:"total_trades"`
			WinningTrades int                           `json:"winning_trades"`
			LosingTrades  int                           `json:"losing_trades"`
			WinRate       float64                       `json:"win_rate"`
			AvgWin        float64                       `json:"avg_win"`
			AvgLoss       float64                       `json:"avg_loss"`
			ProfitFactor  float64                       `json:"profit_factor"`
			SharpeRatio   float64                       `json:"sharpe_ratio"`
			RecentTrades  []TradeOutcome                `json:"recent_trades"`
			SymbolStats   map[string]*SymbolPerformance `json:"symbol_stats"`
			BestSymbol    string                        `json:"best_symbol"`
			WorstSymbol   string                        `json:"worst_symbol"`
		}

		var perfData PerformanceData
		if jsonData, err := json.Marshal(ctx.Performance); err == nil {
			if err := json.Unmarshal(jsonData, &perfData); err == nil {
				sb.WriteString("## � HISTORICAL PERFORMANCE REVIEW (Last 100 Cycles)\n\n")
				sb.WriteString("**⚠️ 重要：以下是你过去的交易表现，请从中学习并避免重复错误。**\n\n")

				// 1. 整体统计
				sb.WriteString("### 📊 Overall Statistics\n\n")
				if perfData.TotalTrades > 0 {
					sb.WriteString(fmt.Sprintf("- **总交易数**: %d (盈利 %d, 亏损 %d)\n",
						perfData.TotalTrades, perfData.WinningTrades, perfData.LosingTrades))
					sb.WriteString(fmt.Sprintf("- **胜率**: %.1f%%\n", perfData.WinRate))
					sb.WriteString(fmt.Sprintf("- **平均盈利**: $%.2f | **平均亏损**: $%.2f\n",
						perfData.AvgWin, perfData.AvgLoss))
					sb.WriteString(fmt.Sprintf("- **盈亏比 (Profit Factor)**: %.2f\n", perfData.ProfitFactor))
					sb.WriteString(fmt.Sprintf("- **夏普比率 (Sharpe Ratio)**: %.2f\n\n", perfData.SharpeRatio))
				} else {
					sb.WriteString("- **总交易数**: 0（暂无历史交易数据）\n\n")
				}

				// 2. 状态提示（基于夏普比率）- 强制执行
				sb.WriteString("### 🎯 Current Trading Mode (MANDATORY)\n\n")
				if perfData.SharpeRatio < -0.5 {
					sb.WriteString("🚨 **状态**: 持续亏损 - **完全禁止开新仓**（只能 close/hold/wait）\n")
					sb.WriteString("**强制规则**: 任何 open_long/open_short 决策都将被拒绝\n\n")
				} else if perfData.SharpeRatio < 0 {
					sb.WriteString("⚠️ **状态**: 轻微亏损 - 收缩模式\n")
					sb.WriteString("**强制规则**: 仓位限制为正常的 50%，杠杆限制为正常的 50%，confidence ≥ 85\n\n")
				} else if perfData.SharpeRatio < 0.7 {
					sb.WriteString("✅ **状态**: 稳健正收益 - 保持当前节奏\n\n")
				} else {
					sb.WriteString("🚀 **状态**: 优异表现 - 可适当扩大仓位（仍需遵守风控）\n\n")
				}

				// 3. 各币种表现（最佳/最差）
				if len(perfData.SymbolStats) > 0 {
					sb.WriteString("### 🏆 Symbol Performance Analysis\n\n")

					if perfData.BestSymbol != "" {
						bestStats := perfData.SymbolStats[perfData.BestSymbol]
						sb.WriteString(fmt.Sprintf("**表现最佳**: %s\n", perfData.BestSymbol))
						sb.WriteString(fmt.Sprintf("  - 交易次数: %d (盈利 %d, 亏损 %d)\n",
							bestStats.TotalTrades, bestStats.WinningTrades, bestStats.LosingTrades))
						sb.WriteString(fmt.Sprintf("  - 胜率: %.1f%% | 总盈亏: $%.2f | 平均盈亏: $%.2f\n\n",
							bestStats.WinRate, bestStats.TotalPnL, bestStats.AvgPnL))
					}

					if perfData.WorstSymbol != "" {
						worstStats := perfData.SymbolStats[perfData.WorstSymbol]
						sb.WriteString(fmt.Sprintf("**表现最差**: %s\n", perfData.WorstSymbol))
						sb.WriteString(fmt.Sprintf("  - 交易次数: %d (盈利 %d, 亏损 %d)\n",
							worstStats.TotalTrades, worstStats.WinningTrades, worstStats.LosingTrades))
						sb.WriteString(fmt.Sprintf("  - 胜率: %.1f%% | 总盈亏: $%.2f | 平均盈亏: $%.2f\n\n",
							worstStats.WinRate, worstStats.TotalPnL, worstStats.AvgPnL))
					}
				}

				// 4. 最近交易记录（最多显示 10 笔）
				if len(perfData.RecentTrades) > 0 {
					sb.WriteString("### 📋 Recent Trades (Last 10)\n\n")
					recentCount := 10
					if len(perfData.RecentTrades) < recentCount {
						recentCount = len(perfData.RecentTrades)
					}

					// 从最新的开始显示
					startIdx := len(perfData.RecentTrades) - recentCount
					for i := startIdx; i < len(perfData.RecentTrades); i++ {
						trade := perfData.RecentTrades[i]
						profitEmoji := "✅"
						if trade.PnL < 0 {
							profitEmoji = "❌"
						} else if trade.PnL == 0 {
							profitEmoji = "➖"
						}

						sb.WriteString(fmt.Sprintf("%s **%s %s**: %.4f → %.4f | PnL: %+.2f%% ($%.2f) | 持仓: %s\n",
							profitEmoji, trade.Symbol, strings.ToUpper(trade.Side),
							trade.OpenPrice, trade.ClosePrice,
							trade.PnLPct, trade.PnL, trade.Duration))
					}
					sb.WriteString("\n")

					// 5. 连续亏损警告（强制执行）
					consecutiveLosses := 0
					for i := len(perfData.RecentTrades) - 1; i >= 0; i-- {
						if perfData.RecentTrades[i].PnL < 0 {
							consecutiveLosses++
						} else {
							break
						}
					}

					if consecutiveLosses >= 3 {
						sb.WriteString(fmt.Sprintf("🚨 **强制警告**: 连续 %d 笔亏损！\n", consecutiveLosses))
						sb.WriteString("**强制规则**: 暂停开新仓 1 个周期，仓位限制为正常的 30%%\n\n")
					}

					// 检查最近 5 笔交易的胜率
					if len(perfData.RecentTrades) >= 5 {
						recentLosses := 0
						for i := len(perfData.RecentTrades) - 5; i < len(perfData.RecentTrades); i++ {
							if perfData.RecentTrades[i].PnL < 0 {
								recentLosses++
							}
						}
						if recentLosses >= 3 {
							sb.WriteString(fmt.Sprintf("⚠️ **警告**: 最近 5 笔中有 %d 笔亏损（胜率 %.0f%%）\n", recentLosses, float64(5-recentLosses)/5*100))
							sb.WriteString("**强制规则**: 仓位限制为正常的 50%%，confidence 门槛提高至 ≥ 85\n\n")
						}
					}
				}

				// 6. 学习要点（强制执行）
				sb.WriteString("### 💡 Key Learnings (MANDATORY)\n\n")
				sb.WriteString("**基于历史表现，你必须**:\n")
				if perfData.WorstSymbol != "" {
					sb.WriteString(fmt.Sprintf("- ❌ **避免**: %s 表现最差，除非有极强信号（confidence ≥ 90）\n", perfData.WorstSymbol))
				}
				if perfData.BestSymbol != "" {
					sb.WriteString(fmt.Sprintf("- ✅ **优先**: %s 表现最佳，可优先考虑该币种的机会\n", perfData.BestSymbol))
				}
				if perfData.WinRate < 50 && perfData.TotalTrades >= 5 {
					sb.WriteString("- ⚠️ **胜率偏低**: 提高开仓门槛（confidence ≥ 80），减少交易频率\n")
				}
				if perfData.ProfitFactor < 1.5 && perfData.TotalTrades >= 5 {
					sb.WriteString("- ⚠️ **盈亏比不佳**: 扩大止盈目标，收紧止损，提高风险回报比\n")
				}
				if len(perfData.RecentTrades) > 0 {
					// 检查最近是否有连续盈利
					consecutiveWins := 0
					for i := len(perfData.RecentTrades) - 1; i >= 0; i-- {
						if perfData.RecentTrades[i].PnL > 0 {
							consecutiveWins++
						} else {
							break
						}
					}
					if consecutiveWins >= 3 {
						sb.WriteString(fmt.Sprintf("- 🎉 **连续 %d 笔盈利**: 保持当前策略，但不要过度自信\n", consecutiveWins))
					}
				}
				sb.WriteString("\n")

				sb.WriteString("---\n\n")
			}
		}
	}

	// === 账户状态 ===
	sb.WriteString("## 💰 ACCOUNT STATUS\n\n")
	sb.WriteString(fmt.Sprintf("- **账户净值**: $%.2f USDT\n", ctx.Account.TotalEquity))
	sb.WriteString(fmt.Sprintf("- **可用余额**: $%.2f USDT (%.1f%% of equity)\n",
		ctx.Account.AvailableBalance,
		(ctx.Account.AvailableBalance/ctx.Account.TotalEquity)*100))
	sb.WriteString(fmt.Sprintf("- **总盈亏**: %+.2f%%\n", ctx.Account.TotalPnLPct))
	sb.WriteString(fmt.Sprintf("- **保证金使用率**: %.1f%% (上限 80%%)\n", ctx.Account.MarginUsedPct))
	sb.WriteString(fmt.Sprintf("- **持仓数量**: %d/3\n\n", ctx.Account.PositionCount))

	// === BTC 市场概览（领先指标）===
	if btcData, hasBTC := ctx.MarketDataMap["BTCUSDT"]; hasBTC {
		sb.WriteString("## 🔍 BTC MARKET OVERVIEW (Market Leader)\n\n")
		sb.WriteString(fmt.Sprintf("- **当前价格**: $%.2f\n", btcData.CurrentPrice))
		sb.WriteString(fmt.Sprintf("- **1小时变化**: %+.2f%%\n", btcData.PriceChange1h))
		sb.WriteString(fmt.Sprintf("- **4小时变化**: %+.2f%%\n", btcData.PriceChange4h))
		sb.WriteString(fmt.Sprintf("- **MACD**: %.4f\n", btcData.CurrentMACD))
		sb.WriteString(fmt.Sprintf("- **RSI(7)**: %.2f\n\n", btcData.CurrentRSI7))

		// 简单的趋势判断
		if btcData.CurrentPrice > btcData.CurrentEMA20 && btcData.CurrentMACD > 0 {
			sb.WriteString("📈 **BTC趋势**: 看涨（价格 > EMA20, MACD > 0）\n\n")
		} else if btcData.CurrentPrice < btcData.CurrentEMA20 && btcData.CurrentMACD < 0 {
			sb.WriteString("📉 **BTC趋势**: 看跌（价格 < EMA20, MACD < 0）\n\n")
		} else {
			sb.WriteString("➡️ **BTC趋势**: 震荡/不明确\n\n")
		}
	}

	sb.WriteString("---\n\n")

	// === 当前持仓（如果有）===
	if len(ctx.Positions) > 0 {
		sb.WriteString("## 📊 CURRENT POSITIONS & PERFORMANCE\n\n")
		for i, pos := range ctx.Positions {
			// 计算持仓时长
			holdingDuration := ""
			if pos.UpdateTime > 0 {
				durationMs := time.Now().UnixMilli() - pos.UpdateTime
				durationMin := durationMs / (1000 * 60)
				if durationMin < 60 {
					holdingDuration = fmt.Sprintf("%d分钟", durationMin)
				} else {
					durationHour := durationMin / 60
					durationMinRemainder := durationMin % 60
					holdingDuration = fmt.Sprintf("%d小时%d分钟", durationHour, durationMinRemainder)
				}
			}

			sb.WriteString(fmt.Sprintf("### Position %d: %s %s\n\n", i+1, pos.Symbol, strings.ToUpper(pos.Side)))
			sb.WriteString(fmt.Sprintf("- **入场价**: %.4f | **当前价**: %.4f\n", pos.EntryPrice, pos.MarkPrice))
			sb.WriteString(fmt.Sprintf("- **未实现盈亏**: %+.2f%%\n", pos.UnrealizedPnLPct))
			sb.WriteString(fmt.Sprintf("- **杠杆**: %dx | **保证金占用**: $%.0f\n", pos.Leverage, pos.MarginUsed))
			sb.WriteString(fmt.Sprintf("- **强平价**: %.4f\n", pos.LiquidationPrice))
			if holdingDuration != "" {
				sb.WriteString(fmt.Sprintf("- **持仓时长**: %s\n", holdingDuration))
			}
			sb.WriteString("\n")

			// 完整市场数据
			if marketData, ok := ctx.MarketDataMap[pos.Symbol]; ok {
				sb.WriteString("**市场数据 (用于评估是否继续持有/平仓)**:\n\n")
				sb.WriteString(market.Format(marketData))
				sb.WriteString("\n")
			}
		}
	} else {
		sb.WriteString("## 📊 CURRENT POSITIONS\n\n")
		sb.WriteString("**无持仓** - 可用资金充足，可寻找新机会\n\n")
	}

	sb.WriteString("---\n\n")

	// === 候选币种市场数据 ===
	sb.WriteString(fmt.Sprintf("## 🎯 CANDIDATE COINS MARKET DATA (%d coins)\n\n", len(ctx.MarketDataMap)))
	sb.WriteString("**以下是所有候选币种的完整市场数据，用于寻找新交易机会。**\n\n")
	sb.WriteString("⚠️ **记住**: 所有序列数据顺序为 **最旧 → 最新**（数组最后一个元素是最新数据）\n\n")

	displayedCount := 0
	for _, coin := range ctx.CandidateCoins {
		marketData, hasData := ctx.MarketDataMap[coin.Symbol]
		if !hasData {
			continue
		}
		displayedCount++

		// 来源标签
		// sourceTags := ""
		// if len(coin.Sources) > 1 {
		// 	sourceTags = " 🔥 (AI500 + OI_Top 双重信号)"
		// } else if len(coin.Sources) == 1 && coin.Sources[0] == "oi_top" {
		// 	sourceTags = " 📈 (OI_Top 持仓增长)"
		// } else if len(coin.Sources) == 1 && coin.Sources[0] == "ai500" {
		// 	sourceTags = " 🤖 (AI500 评分)"
		// }

		// sb.WriteString(fmt.Sprintf("### %d. %s%s\n\n", displayedCount, coin.Symbol, sourceTags))
		sb.WriteString(fmt.Sprintf("### %d. %s\n\n", displayedCount, coin.Symbol))
		sb.WriteString(market.Format(marketData))
		sb.WriteString("\n")
	}

	sb.WriteString("---\n\n")

	// === 最终指令 ===
	sb.WriteString("## 📋 YOUR TASK\n\n")
	sb.WriteString("⚠️ **CRITICAL REMINDER**: You are trading with REAL MONEY. Every decision has REAL consequences.\n\n")
	sb.WriteString("**决策流程（按顺序执行）**:\n\n")
	sb.WriteString("1. **检查历史表现**: 连续亏损？夏普比率？是否被禁止开新仓？\n")
	sb.WriteString("2. **评估现有持仓**（如果有）: 是否需要平仓/继续持有？持仓时长是否 < 30 分钟？\n")
	sb.WriteString("3. **判断 4h 主趋势**: 上升/下降/震荡？BTC 趋势如何？\n")
	sb.WriteString("4. **扫描新机会**（如果有可用资金）: 哪些币种有强信号？是否与 4h 趋势一致？\n")
	sb.WriteString("5. **计算手续费影响**: 每笔交易预期收益是否 > 手续费的 5 倍？\n")
	sb.WriteString("6. **量化 Confidence 评分**: 使用 5 维度评分系统（趋势一致性 + 指标共振 + OI确认 + R:R + 市场环境）\n")
	sb.WriteString("7. **验证强制规则**: 是否违反趋势优先级？是否在冷静期？是否连续亏损？\n")
	sb.WriteString("8. **输出决策**: 先简洁的思维链分析（2-5句话），然后输出JSON决策数组\n\n")
	sb.WriteString("**强制检查清单（违反将导致交易失败）**:\n")
	sb.WriteString("- 🚨 **夏普比率约束**: Sharpe < -0.5 时，完全禁止开新仓\n")
	sb.WriteString("- 🚨 **连续亏损保护**: 连续 3 笔亏损时，暂停开新仓 1 个周期\n")
	sb.WriteString("- 🚨 **趋势优先级**: 禁止使用 3min 信号对抗 4h 主趋势\n")
	sb.WriteString("- 🚨 **最小持仓时间**: 开仓后必须持有至少 30 分钟（除非触发止损/止盈）\n")
	sb.WriteString("- 🚨 **BTC 相关性**: BTC 4h 下跌时，禁止做多山寨币\n\n")
	sb.WriteString("**标准检查清单**:\n")
	sb.WriteString("- ✅ 数据顺序: 最旧 → 最新（数组最后一个元素是最新）\n")
	sb.WriteString("- ✅ 风险回报比: ≥ 1:3（强制要求）\n")
	sb.WriteString("- ✅ 预期收益: > 0.5%（手续费 0.09% 的 5 倍以上）\n")
	sb.WriteString("- ✅ Confidence: ≥ 75（基于量化评分，不能凭感觉）\n")
	sb.WriteString("- ✅ Reasoning: 必须说明 4h 趋势、预期收益、手续费占比、Confidence 计算过程\n\n")
	sb.WriteString("**不确定时选择 wait，不要强行交易。保护资本比追逐收益更重要。**\n\n")

	return sb.String()
}

// parseFullDecisionResponse 解析AI的完整决策响应
func parseFullDecisionResponse(aiResponse string, accountEquity float64, btcEthLeverage, altcoinLeverage int) (*FullDecision, error) {
	// 1. 提取思维链
	cotTrace := extractCoTTrace(aiResponse)

	// 2. 提取JSON决策列表
	decisions, err := extractDecisions(aiResponse)
	if err != nil {
		return &FullDecision{
			CoTTrace:  cotTrace,
			Decisions: []Decision{},
		}, fmt.Errorf("提取决策失败: %w\n\n=== AI思维链分析 ===\n%s", err, cotTrace)
	}

	// 3. 验证决策
	if err := validateDecisions(decisions, accountEquity, btcEthLeverage, altcoinLeverage); err != nil {
		return &FullDecision{
			CoTTrace:  cotTrace,
			Decisions: decisions,
		}, fmt.Errorf("决策验证失败: %w\n\n=== AI思维链分析 ===\n%s", err, cotTrace)
	}

	return &FullDecision{
		CoTTrace:  cotTrace,
		Decisions: decisions,
	}, nil
}

// extractCoTTrace 提取思维链分析
func extractCoTTrace(response string) string {
	// 查找JSON数组的开始位置
	jsonStart := strings.Index(response, "[")

	if jsonStart > 0 {
		// 思维链是JSON数组之前的内容
		return strings.TrimSpace(response[:jsonStart])
	}

	// 如果找不到JSON，整个响应都是思维链
	return strings.TrimSpace(response)
}

// extractDecisions 提取JSON决策列表
func extractDecisions(response string) ([]Decision, error) {
	// 直接查找JSON数组 - 找第一个完整的JSON数组
	arrayStart := strings.Index(response, "[")
	if arrayStart == -1 {
		return nil, fmt.Errorf("无法找到JSON数组起始")
	}

	// 从 [ 开始，匹配括号找到对应的 ]
	arrayEnd := findMatchingBracket(response, arrayStart)
	if arrayEnd == -1 {
		return nil, fmt.Errorf("无法找到JSON数组结束")
	}

	jsonContent := strings.TrimSpace(response[arrayStart : arrayEnd+1])

	// 🔧 修复常见的JSON格式错误：缺少引号的字段值
	// 匹配: "reasoning": 内容"}  或  "reasoning": 内容}  (没有引号)
	// 修复为: "reasoning": "内容"}
	// 使用简单的字符串扫描而不是正则表达式
	jsonContent = fixMissingQuotes(jsonContent)

	// 解析JSON
	var decisions []Decision
	if err := json.Unmarshal([]byte(jsonContent), &decisions); err != nil {
		return nil, fmt.Errorf("JSON解析失败: %w\nJSON内容: %s", err, jsonContent)
	}

	return decisions, nil
}

// fixMissingQuotes 替换中文引号为英文引号（避免输入法自动转换）
func fixMissingQuotes(jsonStr string) string {
	jsonStr = strings.ReplaceAll(jsonStr, "\u201c", "\"") // "
	jsonStr = strings.ReplaceAll(jsonStr, "\u201d", "\"") // "
	jsonStr = strings.ReplaceAll(jsonStr, "\u2018", "'")  // '
	jsonStr = strings.ReplaceAll(jsonStr, "\u2019", "'")  // '
	return jsonStr
}

// validateDecisions 验证所有决策（需要账户信息和杠杆配置）
func validateDecisions(decisions []Decision, accountEquity float64, btcEthLeverage, altcoinLeverage int) error {
	for i, decision := range decisions {
		if err := validateDecision(&decision, accountEquity, btcEthLeverage, altcoinLeverage); err != nil {
			return fmt.Errorf("决策 #%d 验证失败: %w", i+1, err)
		}
	}
	return nil
}

// findMatchingBracket 查找匹配的右括号
func findMatchingBracket(s string, start int) int {
	if start >= len(s) || s[start] != '[' {
		return -1
	}

	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i
			}
		}
	}

	return -1
}

// validateDecision 验证单个决策的有效性
func validateDecision(d *Decision, accountEquity float64, btcEthLeverage, altcoinLeverage int) error {
	// 验证action
	validActions := map[string]bool{
		"open_long":   true,
		"open_short":  true,
		"close_long":  true,
		"close_short": true,
		"hold":        true,
		"wait":        true,
	}

	if !validActions[d.Action] {
		return fmt.Errorf("无效的action: %s", d.Action)
	}

	// 开仓操作必须提供完整参数
	if d.Action == "open_long" || d.Action == "open_short" {
		// 根据币种使用配置的杠杆上限
		maxLeverage := altcoinLeverage          // 山寨币使用配置的杠杆
		maxPositionValue := accountEquity * 1.5 // 山寨币最多1.5倍账户净值
		if d.Symbol == "BTCUSDT" || d.Symbol == "ETHUSDT" {
			maxLeverage = btcEthLeverage          // BTC和ETH使用配置的杠杆
			maxPositionValue = accountEquity * 10 // BTC/ETH最多10倍账户净值
		}

		if d.Leverage <= 0 || d.Leverage > maxLeverage {
			return fmt.Errorf("杠杆必须在1-%d之间（%s，当前配置上限%d倍）: %d", maxLeverage, d.Symbol, maxLeverage, d.Leverage)
		}
		if d.PositionSizeUSD <= 0 {
			return fmt.Errorf("仓位大小必须大于0: %.2f", d.PositionSizeUSD)
		}
		// 验证仓位价值上限（加1%容差以避免浮点数精度问题）
		tolerance := maxPositionValue * 0.01 // 1%容差
		if d.PositionSizeUSD > maxPositionValue+tolerance {
			if d.Symbol == "BTCUSDT" || d.Symbol == "ETHUSDT" {
				return fmt.Errorf("BTC/ETH单币种仓位价值不能超过%.0f USDT（10倍账户净值），实际: %.0f", maxPositionValue, d.PositionSizeUSD)
			} else {
				return fmt.Errorf("山寨币单币种仓位价值不能超过%.0f USDT（1.5倍账户净值），实际: %.0f", maxPositionValue, d.PositionSizeUSD)
			}
		}
		if d.StopLoss <= 0 || d.TakeProfit <= 0 {
			return fmt.Errorf("止损和止盈必须大于0")
		}

		// 验证止损止盈的合理性
		if d.Action == "open_long" {
			if d.StopLoss >= d.TakeProfit {
				return fmt.Errorf("做多时止损价必须小于止盈价")
			}
		} else {
			if d.StopLoss <= d.TakeProfit {
				return fmt.Errorf("做空时止损价必须大于止盈价")
			}
		}

		// 验证风险回报比（必须≥1:3）
		// 计算入场价（假设当前市价）
		var entryPrice float64
		if d.Action == "open_long" {
			// 做多：入场价在止损和止盈之间
			entryPrice = d.StopLoss + (d.TakeProfit-d.StopLoss)*0.2 // 假设在20%位置入场
		} else {
			// 做空：入场价在止损和止盈之间
			entryPrice = d.StopLoss - (d.StopLoss-d.TakeProfit)*0.2 // 假设在20%位置入场
		}

		var riskPercent, rewardPercent, riskRewardRatio float64
		if d.Action == "open_long" {
			riskPercent = (entryPrice - d.StopLoss) / entryPrice * 100
			rewardPercent = (d.TakeProfit - entryPrice) / entryPrice * 100
			if riskPercent > 0 {
				riskRewardRatio = rewardPercent / riskPercent
			}
		} else {
			riskPercent = (d.StopLoss - entryPrice) / entryPrice * 100
			rewardPercent = (entryPrice - d.TakeProfit) / entryPrice * 100
			if riskPercent > 0 {
				riskRewardRatio = rewardPercent / riskPercent
			}
		}

		// 硬约束：风险回报比必须≥2.5
		if riskRewardRatio < 2.5 {
			return fmt.Errorf("风险回报比过低(%.2f:1)，必须≥2.5:1 [风险:%.2f%% 收益:%.2f%%] [止损:%.2f 止盈:%.2f]",
				riskRewardRatio, riskPercent, rewardPercent, d.StopLoss, d.TakeProfit)
		}
	}

	return nil
}
