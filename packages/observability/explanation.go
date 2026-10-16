package observability

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

// Explain only selects curated text. Neither unknown codes nor raw errors are
// interpolated into user-facing reasons or actions.
func Explain(failure Failure) contracts.FailureExplanation {
	explanation := func(reason, action string) contracts.FailureExplanation {
		return contracts.FailureExplanation{Reason: reason, Action: action}
	}
	code := failure.Code
	if semanticCode(code) {
		switch {
		case strings.HasSuffix(code, "_http_401"), strings.HasSuffix(code, "_http_403"):
			return explanation("模型接入认证或权限校验失败。", "请管理员检查模型凭据及访问权限；修复后重新排队已暂停任务。")
		case strings.HasSuffix(code, "_http_429"):
			return explanation("模型服务限流或配额不足。", "稍后重试；持续发生时请管理员检查配额和并发配置。")
		case strings.HasSuffix(code, "_input_too_large"):
			return explanation("模型输入超出接入点的容量限制。", "请管理员检查输入预算和模型容量；修复后重新排队已暂停任务。")
		case strings.HasSuffix(code, "_input_invalid"):
			return explanation("服务端准备构图输入时发现来源或状态不一致，尚未进入模型推理。", "请管理员检查历史导入与已发布工作状态；修复服务端后重试，无需修改模型凭据。")
		case strings.HasSuffix(code, "_source_invalid"), strings.HasSuffix(code, "_source_read_failed"):
			return explanation("固定来源读取或一致性校验失败。", "请管理员检查已保存的来源和索引；保留原始会话，修复后再重试。")
		case strings.HasSuffix(code, "_needs_context"):
			return explanation("现有证据不足以生成可靠结果。", "检查来源内容与上下文准备；修复后重新排队已暂停任务。")
		case strings.Contains(code, "_invalid"), strings.HasSuffix(code, "_empty_content"), strings.HasSuffix(code, "_truncated"), strings.HasSuffix(code, "_response_too_large"), strings.HasSuffix(code, "_refused"):
			return explanation("模型结果未通过格式、内容或图约束校验。", "持续失败时请管理员检查模型接入、输出预算与提示词；修复后重新排队已暂停任务。")
		case strings.Contains(code, "_http_4") && !strings.HasSuffix(code, "_http_408") && !strings.HasSuffix(code, "_http_409") && !strings.HasSuffix(code, "_http_425"):
			return explanation("模型接入点拒绝了请求。", "请管理员检查模型名称、接口及请求配置；修复后重新排队已暂停任务。")
		}
	}
	switch failure.Category {
	case contracts.FailureTimeout:
		return explanation("请求超时，未能及时完成。", "稍后重试；持续发生时检查网络和服务响应时间。")
	case contracts.FailureNetwork:
		return explanation("网络连接失败。", "检查网络与服务地址后重试。")
	case contracts.FailureAuthentication:
		return explanation("身份凭据无效或已过期。", "重新登录或更新当前 Deployment 的凭据。")
	case contracts.FailureAuthorization:
		return explanation("当前身份没有访问权限。", "检查项目成员资格或联系管理员。")
	case contracts.FailureDeploymentIdentity:
		return explanation("本地绑定与 Deployment 身份不一致。", "核对服务身份和项目绑定后重新连接。")
	case contracts.FailureAdapter:
		return explanation("Agent 会话适配器未能完成处理。", "检查受支持的 Agent 版本及会话文件是否可读。")
	case contracts.FailureLocalState:
		return explanation("本地状态无法读写。", "检查目录权限、磁盘空间及本地状态文件。")
	case contracts.FailureArtifactIntegrity:
		return explanation("会话文件完整性校验失败。", "保留原始会话，检查文件传输和存储后重试。")
	case contracts.FailureObjectStore:
		return explanation("会话文件存储服务不可用。", "稍后重试；持续发生时请管理员检查对象存储配置。")
	default:
		return explanation("服务未能完成处理。", "稍后重试；持续发生时提供错误码和关联编号，请管理员检查日志。")
	}
}

func semanticCode(code string) bool {
	for _, prefix := range []string{"bootstrap_", "work_extraction_", "graph_action_", "history_bootstrap_", "history_patch_", "library_checkpoint_", "library_tags_"} {
		if strings.HasPrefix(code, prefix) {
			return true
		}
	}
	return false
}

// AggregateFailures counts only retained events; success does not resolve or
// erase these historical observations. The latest event supplies correlation.
func AggregateFailures(events []Event) []contracts.FailureHealthAggregate {
	type key struct {
		operation string
		status    contracts.Status
		category  contracts.FailureCategory
		code      string
		retryable bool
	}
	groups := map[key]*contracts.FailureHealthAggregate{}
	for _, event := range events {
		if ValidateEvent(event) != nil || event.FailureCategory == "" || event.ErrorCode == "" {
			continue
		}
		k := key{event.Operation, event.Status, event.FailureCategory, event.ErrorCode, event.Retryable}
		item := groups[k]
		if item == nil {
			explanation := Explain(Failure{Category: event.FailureCategory, Code: event.ErrorCode, Retryable: event.Retryable})
			item = &contracts.FailureHealthAggregate{Operation: event.Operation, Status: event.Status, FailureCategory: event.FailureCategory, ErrorCode: event.ErrorCode, Retryable: event.Retryable, Explanation: &explanation}
			groups[k] = item
		}
		item.EventCount++
		if item.LastObservedAt == "" || !before(event.ObservedAt, item.LastObservedAt) {
			item.LastObservedAt, item.CorrelationID = event.ObservedAt, event.CorrelationID
		}
	}
	result := make([]contracts.FailureHealthAggregate, 0, len(groups))
	for _, item := range groups {
		result = append(result, *item)
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if before(a.LastObservedAt, b.LastObservedAt) {
			return false
		}
		if before(b.LastObservedAt, a.LastObservedAt) {
			return true
		}
		return strings.Join([]string{a.Operation, string(a.Status), string(a.FailureCategory), a.ErrorCode, strconv.FormatBool(a.Retryable)}, "\x00") < strings.Join([]string{b.Operation, string(b.Status), string(b.FailureCategory), b.ErrorCode, strconv.FormatBool(b.Retryable)}, "\x00")
	})
	return result
}

func before(a, b string) bool {
	left, _ := time.Parse(time.RFC3339Nano, a)
	right, _ := time.Parse(time.RFC3339Nano, b)
	return left.Before(right)
}
