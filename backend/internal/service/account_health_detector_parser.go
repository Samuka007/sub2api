package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"mime"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	xhtml "golang.org/x/net/html"
)

const (
	accountHealthMaxMailboxBytes     = 4 << 20
	accountHealthMaxMessages         = 100
	accountHealthMaxMessageBytes     = 80_000
	accountHealthMaxHTMLNodes        = 100_000
	accountHealthMaxActiveFormatting = 512
	accountHealthMaxHTMLTextBytes    = accountHealthMaxMailboxBytes
	accountHealthMaxLinkTextNodes    = 256
	accountHealthMaxSrcdocDepth      = 3
	accountHealthMaxSrcdocBytes      = 512 << 10
	accountHealthMaxSrcdocParses     = 128
	accountHealthMaxJSONTokens       = 20_000
	accountHealthMaxJSONContainers   = 5_000
	accountHealthMaxJSONDepth        = 64
	accountHealthMaxLabeledLines     = 2_000
	accountHealthMaxDescriptionBytes = 64 << 10
	accountHealthMaxURLBytes         = 8 << 10
	accountHealthMaxURLSecrets       = 64
)

type accountHealthDescriptionErrorKind string

const (
	accountHealthDescriptionEmpty           accountHealthDescriptionErrorKind = "empty"
	accountHealthDescriptionMissingEmail    accountHealthDescriptionErrorKind = "missing_email"
	accountHealthDescriptionMissingURL      accountHealthDescriptionErrorKind = "missing_url"
	accountHealthDescriptionMultipleRecords accountHealthDescriptionErrorKind = "multiple_records"
	accountHealthDescriptionInvalidURL      accountHealthDescriptionErrorKind = "invalid_url"
	accountHealthDescriptionMixedInvalid    accountHealthDescriptionErrorKind = "mixed_invalid_lines"
)

// accountHealthDescriptionFormatError deliberately carries only a stable reason.
// Group descriptions can contain mailbox credentials and must never reach an error response.
type accountHealthDescriptionFormatError struct {
	kind accountHealthDescriptionErrorKind
}

func (e *accountHealthDescriptionFormatError) Error() string {
	if e == nil {
		return "account health description format error"
	}
	return "account health description format error: " + string(e.kind)
}

type accountHealthMailboxTarget struct {
	email      string
	mailboxURL string
	secrets    []string
}

type accountHealthDetectionStatus string

const (
	accountHealthStatusDeactivated accountHealthDetectionStatus = "deactivated"
	accountHealthStatusWarning     accountHealthDetectionStatus = "warning"
	accountHealthStatusReactivated accountHealthDetectionStatus = "reactivated"
	accountHealthStatusNotFound    accountHealthDetectionStatus = "no_evidence"
	accountHealthStatusMismatch    accountHealthDetectionStatus = "mismatch"
	accountHealthStatusParseError  accountHealthDetectionStatus = "parse_error"
)

type accountHealthDetection struct {
	Status          accountHealthDetectionStatus
	Score           int
	Language        string
	Evidence        []string
	Subject         string
	MessageDate     string
	AssociatedEmail string
	MessagesScanned int
	PagesScanned    int
	PlusDetected    bool
	PlusScore       int
	PlusLanguage    string
	PlusDate        string
	PaymentMethod   string
	LifespanStatus  accountHealthLifespanStatus
	LifespanSeconds *int64
	BanDate         string
	PlusEvidence    []string
}

type accountHealthMailboxPage struct {
	blocks      []string
	nextLinks   []string
	dynamicHint bool
	structured  bool
}

type accountHealthPlusEvidence struct {
	detected        bool
	score           int
	language        string
	date            string
	latestDate      string
	paymentMethod   string
	associatedEmail string
	evidence        []string
}

type accountHealthLifespanStatus string

type accountHealthDateOrder uint8

const (
	accountHealthLifespanUnavailable    accountHealthLifespanStatus = "unavailable"
	accountHealthLifespanActive         accountHealthLifespanStatus = "active"
	accountHealthLifespanEnded          accountHealthLifespanStatus = "ended"
	accountHealthLifespanBanDateUnknown accountHealthLifespanStatus = "ban_date_unknown"
	accountHealthLifespanInvalidDate    accountHealthLifespanStatus = "invalid_date"
)

const (
	accountHealthDateOrderUnknown accountHealthDateOrder = iota
	accountHealthDateOrderBefore
	accountHealthDateOrderEqual
	accountHealthDateOrderAfter
)

const (
	accountHealthEvidenceAccountMismatch        = "account_mismatch"
	accountHealthEvidencePlusAfterBan           = "plus_after_deactivation"
	accountHealthEvidenceDynamicMailbox         = "dynamic_mailbox"
	accountHealthEvidenceReactivated            = "reactivated_notice"
	accountHealthEvidenceDeactivated            = "deactivation_semantics"
	accountHealthEvidenceOpenAI                 = "openai_anchor"
	accountHealthEvidenceAccountUnavailable     = "account_unavailable"
	accountHealthEvidencePolicyViolation        = "policy_violation"
	accountHealthEvidenceAppeal                 = "appeal_available"
	accountHealthEvidenceDeactivationSubject    = "deactivation_subject"
	accountHealthEvidenceWarning                = "warning_notice"
	accountHealthEvidencePlus                   = "plus_marker"
	accountHealthEvidenceSubscriptionSuccess    = "subscription_confirmed"
	accountHealthEvidenceOrderNumber            = "order_number"
	accountHealthEvidencePaymentMethod          = "payment_method"
	accountHealthEvidenceOrderDate              = "order_date"
	accountHealthEvidenceSubscriptionManagement = "subscription_management"
)

type accountHealthLanguageRule struct {
	name        string
	primary     []*regexp.Regexp
	consequence []*regexp.Regexp
	policy      []*regexp.Regexp
	appeal      []*regexp.Regexp
}

type accountHealthPlusLanguageRule struct {
	name     string
	patterns []*regexp.Regexp
}

const accountHealthEmailExpression = `(?i)[A-Z0-9_%+\-]+(?:\.[A-Z0-9_%+\-]+)*@(?:[A-Z0-9](?:[A-Z0-9\-]{0,61}[A-Z0-9])?\.)+[A-Z]{2,63}`

var (
	accountHealthURLPattern        = regexp.MustCompile(`(?i)https?://[^\s<>"'|]*`)
	accountHealthEmailTokenPattern = regexp.MustCompile(`(?i)[A-Z0-9._%+\-]+@[A-Z0-9.\-]+`)
	accountHealthEmailPattern      = regexp.MustCompile(accountHealthEmailExpression)
	accountHealthEmailSyntax       = regexp.MustCompile(`^(?:` + accountHealthEmailExpression + `)$`)
	accountHealthOrderPattern      = regexp.MustCompile(`(?i)\bsub_[A-Z0-9]+\b`)
	accountHealthPlusPattern       = regexp.MustCompile(`(?i)chatgpt\s*plus`)
	accountHealthSubjectLabel      = regexp.MustCompile(`(?i)^(?:subject|主题|主旨|件名)\s*[:：]\s*`)
	accountHealthPaymentStop       = regexp.MustCompile(`(?i)\s{2,}|(?:order|注文|订单|訂單|bestell|commande|pedido|주문)`)

	accountHealthExactSubjectRules = accountHealthRegexps(
		`(?im)^\s*openai\s*[-:\x{2013}\x{2014}]+\s*access\s+deactivated(?:\s*\[[^\]]+\])?\s*$`,
		`(?im)^\s*openai.{0,20}(?:アカウント|账户|帳戶|konto|compte|cuenta|conta|account|계정).{0,25}(?:無効|停用|禁用|封禁|deaktiv|désactiv|desactiv|disattiv|비활성|정지).*$`,
	)
	accountHealthWarningRules = accountHealthRegexps(
		`(?is)warning\s+about\s+your\s+(?:openai\s+)?account`,
		`(?is)important\s+warning.{0,50}(?:account|usage)`,
		`(?is)(?:アカウント|账户|帳戶|konto|compte|cuenta|계정).{0,30}(?:警告|warnung|avertissement|advertencia|경고)`,
	)
	accountHealthReactivatedRules = accountHealthRegexps(
		`(?is)your\s+(?:openai\s+)?account\s+has\s+been\s+reactivated`,
		`(?is)account\s+access\s+has\s+been\s+restored`,
		`(?is)(?:アカウント|账户|帳戶|konto|compte|cuenta|계정).{0,40}(?:再有効化|已恢复|已恢復|reaktiviert|réactivé|reactivada|재활성화)`,
	)
	accountHealthAssociatedEmailRules = accountHealthRegexps(
		`(?is)associated\s+with\s+([A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,})`,
		`(?is)(?:关联|關聯|関連付け).{0,30}?([A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,})`,
		`(?is)(?:asociad[ao]|associé|verknüpft).{0,20}?([A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,})`,
		`(?is)(?:associad[oa]|associat[oa]).{0,20}?([A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,})`,
		`(?is)(?:연결된|연결되어\s*있는|연관된|연동된).{0,20}?([A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,})`,
	)
	accountHealthPlusCancellationRules = accountHealthRegexps(
		`(?is)chatgpt\s+plus.{0,50}subscription.{0,30}(?:has\s+been|was|is)\s+(?:cancel(?:l)?ed|refunded)`,
		`(?is)chatgpt\s+plus.{0,50}サブスクリプション.{0,30}(?:キャンセル|解約)(?:されました|済み)`,
		`(?is)chatgpt\s+plus.{0,40}(?:订阅|訂閱).{0,20}(?:已取消|已退款|取消成功)`,
		`(?is)chatgpt\s+plus.{0,40}(?:abonnement|abo).{0,20}(?:gekündigt|storniert)`,
		`(?is)chatgpt\s+plus.{0,40}abonnement.{0,20}(?:résilié|annulé|remboursé)`,
		`(?is)chatgpt\s+plus.{0,40}suscripci[oó]n.{0,20}(?:cancelada|reembolsada)`,
		`(?is)chatgpt\s+plus.{0,40}assinatura.{0,20}(?:cancelada|reembolsada)`,
		`(?is)chatgpt\s+plus.{0,40}abbonamento.{0,20}(?:annullato|rimborsato)`,
		`(?is)chatgpt\s+plus.{0,40}구독.{0,20}(?:취소|환불)`,
		`(?is)abonnement.{0,40}chatgpt\s+plus.{0,30}(?:résilié|annulé|remboursé)`,
		`(?is)suscripci[oó]n.{0,40}chatgpt\s+plus.{0,30}(?:cancelada|reembolsada)`,
		`(?is)assinatura.{0,40}chatgpt\s+plus.{0,30}(?:cancelada|reembolsada)`,
		`(?is)abbonamento.{0,40}chatgpt\s+plus.{0,30}(?:annullato|rimborsato)`,
	)
	accountHealthPlusFailureRules = accountHealthRegexps(
		`(?is)chatgpt\s+plus.{0,80}(?:payment|renewal|subscription).{0,40}(?:fail(?:ed|ure)?|declined|past[ -]?due|incomplete|unsuccessful|could\s+not\s+be\s+processed)`,
		`(?is)(?:payment|renewal|subscription).{0,80}(?:fail(?:ed|ure)?|declined|past[ -]?due|incomplete|unsuccessful).{0,40}chatgpt\s+plus`,
		`(?is)chatgpt\s+plus.{0,80}(?:not\s+(?:yet\s+)?active|isn['’]?t\s+active|inactive)`,
		`(?is)chatgpt\s+plus.{0,60}(?:支付|付款|续费|續費|订阅|訂閱).{0,30}(?:失败|失敗|被拒|未完成|逾期)`,
		`(?is)chatgpt\s+plus.{0,40}(?:订阅|訂閱).{0,20}(?:尚未|未|没有|沒有)(?:生效|激活|启用|啟用)`,
		`(?is)chatgpt\s+plus.{0,60}(?:支払い|更新|購読).{0,30}(?:失敗|拒否|未完了)`,
		`(?is)chatgpt\s+plus.{0,50}(?:サブスクリプション|購読|登録).{0,30}(?:有効ではありません|有効ではない|まだ有効になっていません|未有効)`,
		`(?is)chatgpt\s+plus.{0,60}(?:zahlung|verlängerung|abonnement).{0,30}(?:fehlgeschlagen|abgelehnt|überfällig)`,
		`(?is)(?:abonnement.{0,40}chatgpt\s+plus|chatgpt\s+plus.{0,40}abonnement).{0,30}(?:noch\s+)?(?:nicht\s+aktiv|inaktiv)`,
		`(?is)chatgpt\s+plus.{0,60}(?:paiement|renouvellement|abonnement).{0,30}(?:échoué|refusé|impayé)`,
		`(?is)chatgpt\s+plus.{0,60}(?:pago|renovaci[oó]n|suscripci[oó]n).{0,30}(?:fallid[oa]|rechazad[oa]|pendiente)`,
		`(?is)chatgpt\s+plus.{0,60}(?:pagamento|renova(?:ção|cao)|assinatura).{0,30}(?:falhou|recusad[oa]|pendente)`,
		`(?is)chatgpt\s+plus.{0,60}(?:pagamento|rinnovo|abbonamento).{0,30}(?:fallit[oa]|rifiutat[oa]|scadut[oa])`,
		`(?is)chatgpt\s+plus.{0,60}(?:결제|갱신|구독).{0,30}(?:실패|거부|미완료|연체)`,
		`(?is)chatgpt\s+plus.{0,40}구독.{0,30}(?:(?:아직\s+)?활성화되지\s+않|비활성)`,
		`(?is)(?:paiement|renouvellement|abonnement).{0,60}(?:échoué|refusé|impayé).{0,40}chatgpt\s+plus`,
		`(?is)(?:pago|renovaci[oó]n|suscripci[oó]n).{0,60}(?:fallid[oa]|rechazad[oa]|pendiente).{0,40}chatgpt\s+plus`,
		`(?is)(?:pagamento|renova(?:ção|cao)|assinatura).{0,60}(?:falhou|recusad[oa]|pendente).{0,40}chatgpt\s+plus`,
		`(?is)(?:pagamento|rinnovo|abbonamento).{0,60}(?:fallit[oa]|rifiutat[oa]|scadut[oa]).{0,40}chatgpt\s+plus`,
		`(?is)(?:abonnement.{0,40}chatgpt\s+plus|chatgpt\s+plus.{0,40}abonnement).{0,30}(?:n['’]?est\s+pas|pas)\s+activ[ée]`,
		`(?is)(?:suscripci[oó]n.{0,40}chatgpt\s+plus|chatgpt\s+plus.{0,40}suscripci[oó]n).{0,30}(?:no\s+est[aá]\s+activ[ao]|inactiv[ao])`,
		`(?is)(?:assinatura.{0,40}chatgpt\s+plus|chatgpt\s+plus.{0,40}assinatura).{0,30}(?:n[aã]o\s+est[aá]\s+ativ[ao]|inativ[ao])`,
		`(?is)(?:abbonamento.{0,40}chatgpt\s+plus|chatgpt\s+plus.{0,40}abbonamento).{0,30}(?:non\s+[èe]\s+attiv[oa]|inattiv[oa])`,
	)
	accountHealthSubscriptionManagementRules = accountHealthRegexps(
		`(?is)manage\s+your\s+subscription`,
		`(?is)cancel\s+at\s+any\s+time`,
		`(?is)自动续订|自動續訂|自動更新|자동\s*갱신|automatisch|renouvel|renovaci|renova|rinnovo`,
	)
	accountHealthNumericDatePattern = regexp.MustCompile(`(?i)(20\d{2})\s*[-/.年]\s*(\d{1,2})\s*[-/.月]\s*(\d{1,2})(?:\s*日)?(?:[ T,，]+(\d{1,2})(?::|时|時|시)(\d{1,2})(?:(?::|分|분)(\d{1,2})(?:[.,](\d{1,9}))?)?(?:\s*((?:UTC|GMT)?[+-]\d{2}:?\d{2}|Z|UTC|GMT))?)?`)
	accountHealthMonthFirstPattern  = regexp.MustCompile(`(?i)([\p{L}]{3,16})\.?\s+(\d{1,2})(?:st|nd|rd|th)?[,]?\s+(20\d{2})(?:\s+(\d{1,2}):(\d{2})(?::(\d{2})(?:[.,](\d{1,9}))?)?(?:\s*((?:UTC|GMT)?[+-]\d{2}:?\d{2}|Z|UTC|GMT))?)?`)
	accountHealthDayFirstPattern    = regexp.MustCompile(`(?i)(\d{1,2})(?:st|nd|rd|th)?\s+(?:de\s+)?([\p{L}]{3,16})\.?(?:\s+de)?[,]?\s+(20\d{2})(?:\s+(\d{1,2}):(\d{2})(?::(\d{2})(?:[.,](\d{1,9}))?)?(?:\s*((?:UTC|GMT)?[+-]\d{2}:?\d{2}|Z|UTC|GMT))?)?`)
)

var accountHealthLanguageRules = []accountHealthLanguageRule{
	{
		name:        "English",
		primary:     accountHealthRegexps(`(?is)your\s+(?:openai\s+)?account\s+has\s+been\s+deactivated`, `(?is)account\s+(?:was|is)\s+deactivated`),
		consequence: accountHealthRegexps(`(?is)can\s+no\s+longer\s+be\s+used`, `(?is)access\s+has\s+been\s+(?:disabled|revoked)`),
		policy:      accountHealthRegexps(`(?is)violat(?:ed|ion).{0,80}(?:terms|usage\s+polic)`, `(?is)terms\s+and\s+usage\s+policies`),
		appeal:      accountHealthRegexps(`(?is)initiate\s+(?:an\s+)?appeal`, `(?is)start\s+an\s+appeal`, `(?is)decision\s+was\s+made\s+in\s+error`),
	},
	{
		name:        "日本語",
		primary:     accountHealthRegexps(`(?is)openai.{0,40}アカウント.{0,60}(?:無効化|無効にな|停止され)`, `(?is)アカウント.{0,30}(?:無効化|停止され)`),
		consequence: accountHealthRegexps(`(?is)(?:アカウント|サービス).{0,50}(?:利用|使用)でき(?:ません|なくな)`, `(?is)アクセス.{0,30}(?:無効|停止)`),
		policy:      accountHealthRegexps(`(?is)(?:利用規約|利用規定).{0,60}違反`, `(?is)違反.{0,60}(?:利用規約|利用規定)`),
		appeal:      accountHealthRegexps(`(?is)異議申(?:し)?立て`, `(?is)誤って無効化`),
	},
	{
		name:        "中文",
		primary:     accountHealthRegexps(`(?is)openai.{0,30}(?:账户|帳戶).{0,50}(?:已被)?(?:停用|禁用|封禁)`, `(?is)(?:账户|帳戶).{0,30}(?:已被)?(?:停用|禁用|封禁)`),
		consequence: accountHealthRegexps(`(?is)(?:无法|無法|不能)再(?:使用|继续使用|繼續使用)`, `(?is)访问权限.{0,20}(?:撤销|停用)`),
		policy:      accountHealthRegexps(`(?is)(?:违反|違反).{0,60}(?:使用条款|使用條款|使用政策|使用規定)`, `(?is)(?:使用条款|使用條款|使用政策).{0,60}(?:违反|違反)`),
		appeal:      accountHealthRegexps(`(?is)(?:发起|提出|開始).{0,20}(?:申诉|申訴|异议|異議)`, `(?is)决定有误`),
	},
	{
		name:        "Deutsch",
		primary:     accountHealthRegexps(`(?is)(?:openai[- ]?)?konto.{0,40}deaktiviert`, `(?is)konto[- ]?deaktivierung`),
		consequence: accountHealthRegexps(`(?is)kann\s+nicht\s+mehr\s+(?:verwendet|genutzt)\s+werden`, `(?is)zugang.{0,30}(?:deaktiviert|gesperrt)`),
		policy:      accountHealthRegexps(`(?is)versto(?:ß|ss).{0,60}(?:nutzungsbedingungen|nutzungsrichtlinien)`, `(?is)nutzungsbedingungen.{0,60}versto`),
		appeal:      accountHealthRegexps(`(?is)(?:einspruch|anfechtung|beschwerde).{0,30}(?:einreichen|starten)`, `(?is)irrtümlich\s+deaktiviert`),
	},
	{
		name:        "Français",
		primary:     accountHealthRegexps(`(?is)compte.{0,30}(?:a\s+été\s+)?désactivé`, `(?is)désactivation.{0,20}(?:du|de\s+votre)\s+compte`),
		consequence: accountHealthRegexps(`(?is)ne\s+peut\s+plus\s+être\s+utilisé`, `(?is)accès.{0,30}(?:désactivé|révoqué)`),
		policy:      accountHealthRegexps(`(?is)viol(?:é|ation).{0,60}(?:conditions|politiques).{0,30}utilisation`, `(?is)conditions\s+d.utilisation.{0,60}viol`),
		appeal:      accountHealthRegexps(`(?is)(?:faire\s+appel|contester|appel).{0,40}(?:décision|désactivation)`, `(?is)désactivé\s+par\s+erreur`),
	},
	{
		name:        "Español",
		primary:     accountHealthRegexps(`(?is)cuenta.{0,30}(?:ha\s+sido|fue|está)\s+desactivada`, `(?is)desactivación.{0,20}(?:de\s+la|de\s+tu)\s+cuenta`),
		consequence: accountHealthRegexps(`(?is)ya\s+no\s+(?:se\s+)?puede\s+utilizar`, `(?is)acceso.{0,30}(?:desactivado|revocado)`),
		policy:      accountHealthRegexps(`(?is)infring(?:ió|ido|imiento).{0,60}(?:términos|políticas).{0,30}uso`, `(?is)términos\s+y\s+políticas\s+de\s+uso`),
		appeal:      accountHealthRegexps(`(?is)(?:iniciar|presentar).{0,20}(?:una\s+)?apelación`, `(?is)desactivada\s+por\s+error`),
	},
	{
		name:        "Português",
		primary:     accountHealthRegexps(`(?is)conta.{0,30}(?:foi|está)\s+desativada`, `(?is)desativação.{0,20}(?:da|de\s+sua)\s+conta`),
		consequence: accountHealthRegexps(`(?is)não\s+pode\s+mais\s+ser\s+usada`, `(?is)acesso.{0,30}(?:desativado|revogado)`),
		policy:      accountHealthRegexps(`(?is)viol(?:ou|ação).{0,60}(?:termos|políticas).{0,30}uso`),
		appeal:      accountHealthRegexps(`(?is)(?:iniciar|enviar).{0,20}(?:uma\s+)?contestação`, `(?is)desativada\s+por\s+engano`),
	},
	{
		name:        "Italiano",
		primary:     accountHealthRegexps(`(?is)account.{0,30}(?:è\s+stato|risulta)\s+disattivato`, `(?is)disattivazione.{0,20}(?:dell|del\s+tuo)\s+account`),
		consequence: accountHealthRegexps(`(?is)non\s+può\s+più\s+essere\s+utilizzato`, `(?is)accesso.{0,30}(?:disattivato|revocato)`),
		policy:      accountHealthRegexps(`(?is)viol(?:ato|azione).{0,60}(?:termini|norme).{0,30}utilizzo`),
		appeal:      accountHealthRegexps(`(?is)(?:avviare|presentare).{0,20}(?:un\s+)?ricorso`, `(?is)disattivato\s+per\s+errore`),
	},
	{
		name:        "한국어",
		primary:     accountHealthRegexps(`(?is)openai.{0,30}계정.{0,40}(?:비활성화|정지)`, `(?is)계정.{0,30}(?:비활성화|정지되었)`),
		consequence: accountHealthRegexps(`(?is)더\s+이상.{0,20}(?:사용|이용)할\s+수\s+없`, `(?is)접근.{0,20}(?:비활성화|차단)`),
		policy:      accountHealthRegexps(`(?is)(?:이용\s+약관|사용\s+정책).{0,50}위반`, `(?is)위반.{0,50}(?:이용\s+약관|사용\s+정책)`),
		appeal:      accountHealthRegexps(`(?is)이의.{0,10}제기`, `(?is)잘못.{0,20}비활성화`),
	},
}

var accountHealthPlusLanguageRules = []accountHealthPlusLanguageRule{
	{name: "English", patterns: accountHealthRegexps(`(?is)successfully\s+subscribed\s+to\s+chatgpt\s+plus`, `(?im)^(?:your\s+)?chatgpt\s+plus\s+subscription\s+(?:is|has\s+been)\s+(?:now\s+)?(?:active|confirmed)(?:[.!]|$)`)},
	{name: "日本語", patterns: accountHealthRegexps(`(?is)chatgpt\s+plus.{0,50}(?:正常に|無事に|成功に|成功裏に)?(?:登録|購読)(?:されました|済み|完了|有効)`, `(?is)chatgpt\s+plus.{0,50}サブスクリプション.{0,30}(?:登録|完了|有効|開始)`)},
	{name: "中文", patterns: accountHealthRegexps(`(?is)(?:成功|已)(?:订阅|訂閱).{0,30}chatgpt\s+plus`, `(?is)chatgpt\s+plus.{0,30}(?:订阅|訂閱).{0,20}(?:成功|生效)`)},
	{name: "Deutsch", patterns: accountHealthRegexps(`(?is)(?:erfolgreich|bestätigt).{0,50}chatgpt\s+plus`, `(?is)chatgpt\s+plus.{0,30}(?:erfolgreich.{0,15}abonniert|abonnement.{0,20}(?:bestätigt|aktiv))`)},
	{name: "Français", patterns: accountHealthRegexps(`(?is)abonn[ée](?:e?s?)?\s+(?:à|au).{0,50}chatgpt\s+plus`, `(?is)abonnement.{0,40}chatgpt\s+plus.{0,25}(?:confirm[ée]|activ[ée])`, `(?is)chatgpt\s+plus.{0,30}abonnement.{0,20}(?:confirm[ée]|activ[ée])`)},
	{name: "Español", patterns: accountHealthRegexps(`(?is)suscri(?:to|ta).{0,50}chatgpt\s+plus`, `(?is)suscripci[oó]n.{0,40}chatgpt\s+plus.{0,25}(?:confirmada|activa)`, `(?is)chatgpt\s+plus.{0,30}suscri(?:pci[oó]n|to|ta).{0,20}(?:confirmada|activa)`)},
	{name: "Português", patterns: accountHealthRegexps(`(?is)assinatura.{0,40}chatgpt\s+plus.{0,25}(?:confirmada|ativa)`, `(?is)chatgpt\s+plus.{0,30}assinatura.{0,20}(?:confirmada|ativa)`)},
	{name: "Italiano", patterns: accountHealthRegexps(`(?is)abbonamento.{0,40}chatgpt\s+plus.{0,25}(?:confermat[oa]|attiv[oa])`, `(?is)chatgpt\s+plus.{0,30}abbonamento.{0,20}(?:confermat[oa]|attiv[oa])`)},
	{name: "한국어", patterns: accountHealthRegexps(`(?is)chatgpt\s+plus.{0,40}구독.{0,20}(?:완료|활성)`)},
}

var accountHealthMonthNumbers = map[string]time.Month{
	"jan": 1, "january": 1, "januar": 1, "janvier": 1, "enero": 1, "janeiro": 1, "gennaio": 1,
	"feb": 2, "february": 2, "februar": 2, "février": 2, "fevrier": 2, "febrero": 2, "fevereiro": 2, "febbraio": 2,
	"mar": 3, "march": 3, "märz": 3, "marz": 3, "mars": 3, "marzo": 3, "março": 3, "marco": 3,
	"apr": 4, "april": 4, "avril": 4, "abril": 4, "aprile": 4,
	"may": 5, "mai": 5, "mayo": 5, "maio": 5, "maggio": 5,
	"jun": 6, "june": 6, "juni": 6, "juin": 6, "junio": 6, "junho": 6, "giugno": 6,
	"jul": 7, "july": 7, "juli": 7, "juillet": 7, "julio": 7, "julho": 7, "luglio": 7,
	"aug": 8, "august": 8, "août": 8, "aout": 8, "agosto": 8,
	"sep": 9, "sept": 9, "september": 9, "septembre": 9, "septiembre": 9, "setembro": 9, "settembre": 9,
	"oct": 10, "october": 10, "oktober": 10, "octobre": 10, "octubre": 10, "outubro": 10, "ottobre": 10,
	"nov": 11, "november": 11, "novembre": 11, "noviembre": 11, "novembro": 11,
	"dec": 12, "december": 12, "dezember": 12, "décembre": 12, "decembre": 12, "diciembre": 12, "dezembro": 12, "dicembre": 12,
}

var accountHealthPlusDateLabels = []string{
	`order\s+date`, `注文日`, `(?:订单|訂單)日期`, `bestelldatum`, `date\s+de\s+commande`,
	`fecha\s+del\s+pedido`, `data\s+do\s+pedido`, `data\s+dell['’]ordine`, `주문\s*날짜`,
}

var accountHealthPaymentLabels = []string{
	`payment\s+method`, `お支払い方法|支払い方法|決済方法`, `付款方式`, `zahlungsmethode`,
	`mode\s+de\s+paiement`, `m[eé]todo\s+de\s+pago`, `m[eé]todo\s+de\s+pagamento`,
	`metodo\s+di\s+pagamento`, `결제\s*수단`,
}

var (
	errAccountHealthMailboxResponseTooLarge = errors.New("account health mailbox response exceeds size limit")
	errAccountHealthMailboxJSONTooComplex   = errors.New("account health mailbox JSON exceeds complexity limit")
)

func accountHealthRegexps(patterns ...string) []*regexp.Regexp {
	result := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		result = append(result, regexp.MustCompile(pattern))
	}
	return result
}

func findAccountHealthEmail(value string) string {
	for _, candidate := range accountHealthEmailTokenPattern.FindAllString(value, -1) {
		if accountHealthEmailSyntax.MatchString(candidate) {
			return strings.ToLower(candidate)
		}
	}
	return ""
}

func parseAccountHealthMailboxTarget(description string) (accountHealthMailboxTarget, error) {
	cleaned := strings.ReplaceAll(stdhtml.UnescapeString(description), "\ufeff", "")
	if len(cleaned) > accountHealthMaxDescriptionBytes {
		return accountHealthMailboxTarget{}, &accountHealthDescriptionFormatError{kind: accountHealthDescriptionInvalidURL}
	}
	cleaned = strings.NewReplacer(
		"\r\n", "\n",
		"\r", "\n",
		"\f", "\n",
		"\v", "\n",
		"\u0085", "\n",
		"\u2028", "\n",
		"\u2029", "\n",
	).Replace(cleaned)
	lines := make([]string, 0, 1)
	for _, raw := range strings.Split(cleaned, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.EqualFold(line, "卡密导出") || strings.EqualFold(line, "账号导出") {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return accountHealthMailboxTarget{}, &accountHealthDescriptionFormatError{kind: accountHealthDescriptionEmpty}
	}
	if len(lines) > 1 {
		validRecords := 0
		for _, line := range lines {
			if _, err := parseAccountHealthMailboxTarget(line); err == nil {
				validRecords++
			}
		}
		kind := accountHealthDescriptionMixedInvalid
		if validRecords == len(lines) {
			kind = accountHealthDescriptionMultipleRecords
		}
		return accountHealthMailboxTarget{}, &accountHealthDescriptionFormatError{kind: kind}
	}

	line := lines[0]
	fields := strings.SplitN(line, "---", 5)
	structured := len(fields) >= 4
	if accountHealthHasAdditionalRecord(line) {
		return accountHealthMailboxTarget{}, &accountHealthDescriptionFormatError{kind: accountHealthDescriptionMultipleRecords}
	}
	prefix := line
	mailboxField := line
	if structured {
		prefix = strings.Join(fields[:3], "---")
		mailboxField = fields[3]
	}

	urlScanField := strings.ReplaceAll(mailboxField, "---", "   ")
	urlMatches := accountHealthURLPattern.FindAllStringIndex(urlScanField, -1)
	if len(urlMatches) == 0 {
		return accountHealthMailboxTarget{}, &accountHealthDescriptionFormatError{kind: accountHealthDescriptionMissingURL}
	}

	firstMatch := urlMatches[0]
	mailboxURL, parsedURL, parseErr := parseAccountHealthDescriptionURL(mailboxField[firstMatch[0]:firstMatch[1]])
	if parseErr != nil {
		return accountHealthMailboxTarget{}, &accountHealthDescriptionFormatError{kind: accountHealthDescriptionInvalidURL}
	}
	if parsedURL.Scheme != "https" {
		return accountHealthMailboxTarget{}, &accountHealthDescriptionFormatError{kind: accountHealthDescriptionInvalidURL}
	}
	if !structured {
		prefix = mailboxField[:firstMatch[0]]
	}

	var email string
	if structured {
		firstField, _, _ := strings.Cut(prefix, "---")
		firstField = strings.TrimSpace(firstField)
		if candidate := findAccountHealthEmail(firstField); strings.EqualFold(candidate, firstField) {
			email = candidate
		}
	} else {
		email = findAccountHealthEmail(strings.TrimRight(strings.TrimSpace(prefix), "-|"))
		if email == "" {
			email = accountHealthEmailFromURL(parsedURL)
		}
	}
	if email == "" {
		return accountHealthMailboxTarget{}, &accountHealthDescriptionFormatError{kind: accountHealthDescriptionMissingEmail}
	}
	secrets := accountHealthDescriptionSecrets(prefix, structured)
	if _, complete := accountHealthRedactionSecrets(mailboxURL, secrets...); !complete {
		return accountHealthMailboxTarget{}, &accountHealthDescriptionFormatError{kind: accountHealthDescriptionInvalidURL}
	}
	return accountHealthMailboxTarget{
		email:      email,
		mailboxURL: mailboxURL,
		secrets:    secrets,
	}, nil
}

func accountHealthHasAdditionalRecord(line string) bool {
	fields := strings.Split(line, "---")
	trimmedFields := make([]string, len(fields))
	urlMatchesByField := make([][]string, len(fields))
	hasURLAfter := make([]bool, len(fields))
	seenURL := false
	for index := len(fields) - 1; index >= 0; index-- {
		hasURLAfter[index] = seenURL
		trimmedFields[index] = strings.TrimSpace(fields[index])
		urlMatchesByField[index] = accountHealthURLPattern.FindAllString(trimmedFields[index], -1)
		seenURL = seenURL || len(urlMatchesByField[index]) > 0
	}

	mailboxURLSeen := false
	firstRecordEmail := ""
	for index, field := range trimmedFields {
		urlMatches := urlMatchesByField[index]
		if len(urlMatches) > 0 {
			if !mailboxURLSeen {
				firstRecordEmail = findAccountHealthEmail(strings.Join(fields[:index], "---"))
			}
			for _, rawURL := range urlMatches {
				parsed, parseErr := url.Parse(strings.TrimRight(rawURL, ".,;，。；)]}>"))
				candidateEmail := ""
				if parseErr == nil {
					candidateEmail = accountHealthEmailFromURL(parsed)
				}
				if !mailboxURLSeen {
					mailboxURLSeen = true
					if firstRecordEmail == "" {
						firstRecordEmail = candidateEmail
					}
					continue
				}
				if candidateEmail != "" && firstRecordEmail != "" &&
					!strings.EqualFold(candidateEmail, firstRecordEmail) {
					return true
				}
			}
			continue
		}
		if !mailboxURLSeen {
			continue
		}
		email := findAccountHealthEmail(field)
		if email == "" || !strings.EqualFold(email, field) {
			continue
		}
		if hasURLAfter[index] {
			return true
		}
	}
	return false
}

func parseAccountHealthDescriptionURL(rawURL string) (string, *url.URL, error) {
	rawURL = strings.TrimRight(strings.TrimSpace(rawURL), ".,;，。；)]}>")
	if rawURL == "" || len(rawURL) > accountHealthMaxURLBytes {
		return "", nil, errAccountHealthUnsafeMailboxURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return "", nil, errAccountHealthUnsafeMailboxURL
	}
	if err := validateAccountHealthPort(parsed); err != nil {
		return "", nil, errAccountHealthUnsafeMailboxURL
	}
	return rawURL, parsed, nil
}

func accountHealthEmailFromURL(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}
	if email := accountHealthEmailFromRawQuery(parsed.RawQuery); email != "" {
		return email
	}
	decodedPath, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil {
		return ""
	}
	return findAccountHealthEmail(decodedPath)
}

func accountHealthDescriptionSecrets(prefix string, structured bool) []string {
	secrets := make([]string, 0, 2)
	for index, field := range strings.Split(prefix, "---") {
		if len(secrets) >= accountHealthMaxURLSecrets {
			break
		}
		value := strings.TrimSpace(field)
		if structured {
			if index == 0 || value == "" {
				continue
			}
		} else {
			value = strings.TrimSpace(accountHealthEmailPattern.ReplaceAllString(value, ""))
		}
		if value == "" || (!structured && accountHealthURLPattern.MatchString(value)) {
			continue
		}
		duplicate := false
		for _, secret := range secrets {
			if secret == value {
				duplicate = true
				break
			}
		}
		if !duplicate {
			secrets = append(secrets, value)
		}
	}
	return secrets
}

func accountHealthEmailFromRawQuery(rawQuery string) string {
	type queryValue struct {
		key   string
		value string
	}
	values := make([]queryValue, 0, strings.Count(rawQuery, "&")+1)
	for _, field := range strings.Split(rawQuery, "&") {
		rawKey, rawValue, _ := strings.Cut(field, "=")
		key, keyErr := url.QueryUnescape(rawKey)
		if keyErr != nil {
			continue
		}
		// QueryUnescape follows form semantics and normally turns a literal '+'
		// into a space. Mailbox exports use '+' literally for email sub-addresses.
		value, valueErr := url.QueryUnescape(strings.ReplaceAll(rawValue, "+", "%2B"))
		if valueErr != nil {
			continue
		}
		values = append(values, queryValue{key: key, value: value})
	}

	for _, wantedKey := range []string{"mail", "email", "address", "user", "login"} {
		for _, candidate := range values {
			if !strings.EqualFold(candidate.key, wantedKey) {
				continue
			}
			if email := findAccountHealthEmail(candidate.value); email != "" {
				return email
			}
		}
	}
	return ""
}

func parseAccountHealthMailboxPage(body []byte, contentType string, maxMessages int) (accountHealthMailboxPage, error) {
	if len(body) > accountHealthMaxMailboxBytes {
		return accountHealthMailboxPage{}, errAccountHealthMailboxResponseTooLarge
	}
	limit := maxMessages
	if limit <= 0 {
		limit = 20
	}
	if limit > accountHealthMaxMessages {
		limit = accountHealthMaxMessages
	}
	trimmed := strings.TrimSpace(string(body))
	mediaType, _, mediaTypeErr := mime.ParseMediaType(contentType)
	isJSONContentType := mediaTypeErr == nil && isAccountHealthJSONMediaType(mediaType)
	isSniffedJSON := strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
	if isJSONContentType || isSniffedJSON {
		page, err := parseAccountHealthJSONPage(body, limit)
		if err == nil {
			return page, nil
		}
		if isJSONContentType {
			return accountHealthMailboxPage{}, err
		}
	}
	return parseAccountHealthHTMLPage(body, limit)
}

func parseAccountHealthJSONPage(body []byte, limit int) (accountHealthMailboxPage, error) {
	if err := validateAccountHealthJSONComplexity(body); err != nil {
		return accountHealthMailboxPage{}, fmt.Errorf("parse account health mailbox JSON: %w", err)
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return accountHealthMailboxPage{}, fmt.Errorf("parse account health mailbox JSON: %w", err)
	}
	blocks := make([]string, 0, limit)
	fingerprints := make(map[string]struct{}, limit)
	budget := newAccountHealthHTMLTextBudget(accountHealthMaxHTMLNodes)
	accountHealthJSONBlocks(value, limit, &blocks, fingerprints, budget)
	return accountHealthMailboxPage{blocks: blocks, structured: true}, nil
}

func validateAccountHealthJSONComplexity(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	tokens := 0
	containers := 0
	depth := 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			if tokens == 0 || depth != 0 {
				return errors.New("invalid account health mailbox JSON")
			}
			return nil
		}
		if err != nil {
			return err
		}
		tokens++
		if tokens > accountHealthMaxJSONTokens {
			return errAccountHealthMailboxJSONTooComplex
		}
		delim, ok := token.(json.Delim)
		if !ok {
			continue
		}
		switch delim {
		case '{', '[':
			containers++
			if containers > accountHealthMaxJSONContainers {
				return errAccountHealthMailboxJSONTooComplex
			}
			depth++
			if depth > accountHealthMaxJSONDepth {
				return errAccountHealthMailboxJSONTooComplex
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return errors.New("invalid account health mailbox JSON")
			}
		}
	}
}

func parseAccountHealthHTMLPage(body []byte, limit int) (accountHealthMailboxPage, error) {
	source := string(body)
	if !accountHealthHTMLWithinNodeLimit(strings.NewReader(source), accountHealthMaxHTMLNodes) {
		return accountHealthMailboxPage{}, nil
	}
	doc, err := xhtml.Parse(strings.NewReader(source))
	if err != nil {
		return accountHealthMailboxPage{}, fmt.Errorf("parse account health mailbox HTML: %w", err)
	}
	budget := newAccountHealthHTMLTextBudget(accountHealthMaxHTMLNodes)
	scan := accountHealthScanHTML(doc, budget)
	page := accountHealthMailboxPage{dynamicHint: scan.dynamicHint}
	fingerprints := make(map[string]struct{}, limit)

	nodes := accountHealthMessageNodes(
		scan.messageContainers,
		scan.articles,
		scan.articleMessageOverlaps,
	)
	messageRoots := make([]*xhtml.Node, 0, len(scan.messageContainers)+len(scan.articles))
	messageRoots = append(messageRoots, scan.messageContainers...)
	messageRoots = append(messageRoots, scan.articles...)
	if len(nodes) == 0 {
		var headingRoots []*xhtml.Node
		page.blocks, headingRoots = accountHealthHeadingBlocks(scan.headings, limit, fingerprints, budget)
		messageRoots = append(messageRoots, headingRoots...)
		if len(page.blocks) == 0 {
			text := accountHealthVisibleTextWithBudget(doc, budget, accountHealthMaxMessageBytes)
			if text != "" {
				page.blocks = appendUniqueAccountHealthBlock(page.blocks, fingerprints, text)
				messageRoots = append(messageRoots, doc)
			}
		}
	} else {
		for _, node := range nodes {
			if len(page.blocks) >= limit {
				break
			}
			text := accountHealthVisibleTextWithBudget(node, budget, accountHealthMaxMessageBytes)
			if text == "" {
				continue
			}
			page.blocks = appendUniqueAccountHealthBlock(page.blocks, fingerprints, text)
		}
	}

	unsafeLinks := accountHealthLinksIntersectingMessageRoots(messageRoots)
	for _, link := range scan.links {
		if len(page.nextLinks) >= 10 {
			break
		}
		if _, unsafe := unsafeLinks[link]; unsafe {
			continue
		}
		href, ok := accountHealthHTMLAttribute(link, "href")
		if ok && len(href) <= accountHealthMaxURLBytes && accountHealthIsNextLink(link, budget) {
			page.nextLinks = append(page.nextLinks, href)
		}
	}
	return page, nil
}

func accountHealthMessageNodes(
	messageContainers, articles []*xhtml.Node,
	articleMessageOverlaps map[*xhtml.Node]struct{},
) []*xhtml.Node {
	nodes := make([]*xhtml.Node, 0, len(messageContainers)+len(articles))
	seen := make(map[*xhtml.Node]struct{}, len(messageContainers)+len(articles))
	for _, node := range messageContainers {
		if node == nil {
			continue
		}
		if _, exists := seen[node]; exists {
			continue
		}
		seen[node] = struct{}{}
		nodes = append(nodes, node)
	}
	if len(nodes) > 0 {
		return nodes
	}
	for _, article := range articles {
		if article == nil {
			continue
		}
		if _, exists := seen[article]; exists {
			continue
		}
		if _, overlapsMessage := articleMessageOverlaps[article]; overlapsMessage {
			continue
		}
		seen[article] = struct{}{}
		nodes = append(nodes, article)
	}
	return nodes
}

func accountHealthJSONBlocks(
	value any,
	limit int,
	blocks *[]string,
	fingerprints map[string]struct{},
	budget *accountHealthHTMLTextBudget,
) {
	if len(*blocks) >= limit {
		return
	}
	switch item := value.(type) {
	case map[string]any:
		preferredKeys := []string{"subject", "title", "from", "sender", "date", "time", "text", "body", "html", "content"}
		preferred := make([]string, 0, len(preferredKeys))
		consumed := make(map[string]struct{}, len(preferredKeys))
		for _, key := range preferredKeys {
			if raw, ok := item[key]; ok {
				if text := accountHealthScalarText(raw, budget); text != "" {
					preferred = append(preferred, text)
					consumed[key] = struct{}{}
				}
			}
		}
		if len(preferred) > 0 {
			text := normalizeAccountHealthText(strings.Join(preferred, "\n"))
			if text != "" && len(text) <= accountHealthMaxMessageBytes {
				*blocks = appendUniqueAccountHealthBlock(*blocks, fingerprints, text)
			}
		}
		containerKeys := make([]string, 0, len(item))
		scalarKeys := make([]string, 0, len(item))
		for key := range item {
			if _, ok := consumed[key]; ok {
				continue
			}
			if accountHealthJSONContainer(item[key]) {
				containerKeys = append(containerKeys, key)
				continue
			}
			if len(preferred) == 0 && !accountHealthJSONMetadataScalar(key, item[key]) {
				scalarKeys = append(scalarKeys, key)
			}
		}
		sort.Strings(containerKeys)
		keys := containerKeys
		if len(containerKeys) == 0 {
			sort.Strings(scalarKeys)
			keys = scalarKeys
		}
		for _, key := range keys {
			accountHealthJSONBlocks(item[key], limit, blocks, fingerprints, budget)
			if len(*blocks) >= limit {
				return
			}
		}
	case []any:
		for _, child := range item {
			accountHealthJSONBlocks(child, limit, blocks, fingerprints, budget)
			if len(*blocks) >= limit {
				return
			}
		}
	case string:
		if text := accountHealthScalarText(item, budget); text != "" && len(text) <= accountHealthMaxMessageBytes {
			*blocks = appendUniqueAccountHealthBlock(*blocks, fingerprints, text)
		}
	case float64, json.Number:
		if text := fmt.Sprint(item); text != "" {
			*blocks = appendUniqueAccountHealthBlock(*blocks, fingerprints, text)
		}
	}
}

func accountHealthJSONContainer(value any) bool {
	switch value.(type) {
	case map[string]any, []any:
		return true
	default:
		return false
	}
}

func accountHealthJSONMetadataScalar(key string, value any) bool {
	if accountHealthJSONContainer(value) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "id", "total", "count", "page", "pages", "page_size", "pagesize", "limit", "offset":
		return true
	default:
		return false
	}
}

func accountHealthScalarText(value any, budget *accountHealthHTMLTextBudget) string {
	text, ok := value.(string)
	if !ok {
		switch value.(type) {
		case float64, json.Number:
			return fmt.Sprint(value)
		default:
			return ""
		}
	}
	if budget == nil || budget.remainingNodes <= 0 || budget.remainingTextBytes <= 0 {
		return ""
	}
	if !accountHealthHTMLWithinNodeLimit(strings.NewReader(text), budget.remainingNodes) {
		budget.remainingNodes = 0
		return ""
	}
	doc, err := xhtml.Parse(strings.NewReader(text))
	if err != nil {
		if !budget.takeNode() || len(text) > budget.remainingTextBytes {
			budget.remainingTextBytes = 0
			return ""
		}
		budget.remainingTextBytes -= len(text)
		if len(text) > accountHealthMaxMessageBytes {
			return ""
		}
		return normalizeAccountHealthText(text)
	}
	return accountHealthVisibleTextWithBudget(doc, budget, accountHealthMaxMessageBytes)
}

type accountHealthHTMLTextBudget struct {
	remainingNodes        int
	remainingTextBytes    int
	remainingSrcdocBytes  int
	remainingSrcdocParses int
}

func newAccountHealthHTMLTextBudget(nodes int) *accountHealthHTMLTextBudget {
	if nodes <= 0 || nodes > accountHealthMaxHTMLNodes {
		nodes = accountHealthMaxHTMLNodes
	}
	return &accountHealthHTMLTextBudget{
		remainingNodes:        nodes,
		remainingTextBytes:    accountHealthMaxHTMLTextBytes,
		remainingSrcdocBytes:  accountHealthMaxSrcdocBytes,
		remainingSrcdocParses: accountHealthMaxSrcdocParses,
	}
}

func (budget *accountHealthHTMLTextBudget) takeNode() bool {
	if budget == nil || budget.remainingNodes <= 0 {
		return false
	}
	budget.remainingNodes--
	return true
}

type accountHealthHTMLTextFrame struct {
	node        *xhtml.Node
	srcdocDepth int
}

func accountHealthVisibleTextWithBudget(root *xhtml.Node, budget *accountHealthHTMLTextBudget, maxBytes int) string {
	maxNodes := 0
	if budget != nil {
		maxNodes = budget.remainingNodes
	}
	return accountHealthVisibleTextWithLimits(root, budget, maxBytes, maxNodes)
}

func accountHealthVisibleTextWithLimits(root *xhtml.Node, budget *accountHealthHTMLTextBudget, maxBytes, maxNodes int) string {
	if root == nil || budget == nil || budget.remainingNodes <= 0 || budget.remainingTextBytes <= 0 || maxBytes <= 0 || maxNodes <= 0 {
		return ""
	}
	parts := make([]string, 0, 16)
	textBytes := 0
	visitedNodes := 0
	stack := []accountHealthHTMLTextFrame{{node: root}}
	for len(stack) > 0 && visitedNodes < maxNodes && budget.remainingNodes > 0 && budget.remainingTextBytes > 0 {
		frame := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		node := frame.node
		if !budget.takeNode() {
			break
		}
		visitedNodes++

		if node.Type == xhtml.ElementNode {
			switch node.Data {
			case "script", "style", "noscript", "svg", "template":
				continue
			case "iframe":
				if frame.srcdocDepth >= accountHealthMaxSrcdocDepth {
					continue
				}
				srcdoc, ok := accountHealthHTMLAttribute(node, "srcdoc")
				if !ok || len(srcdoc) == 0 || budget.remainingSrcdocParses <= 0 {
					continue
				}
				if len(srcdoc) > budget.remainingSrcdocBytes {
					budget.remainingSrcdocBytes = 0
					continue
				}
				budget.remainingSrcdocBytes -= len(srcdoc)
				budget.remainingSrcdocParses--
				remainingFrameNodes := maxNodes - visitedNodes
				if remainingFrameNodes > budget.remainingNodes {
					remainingFrameNodes = budget.remainingNodes
				}
				if !accountHealthHTMLWithinNodeLimit(strings.NewReader(srcdoc), remainingFrameNodes) {
					budget.remainingNodes -= remainingFrameNodes
					visitedNodes += remainingFrameNodes
					continue
				}
				embedded, parseErr := xhtml.Parse(strings.NewReader(srcdoc))
				if parseErr == nil {
					stack = append(stack, accountHealthHTMLTextFrame{
						node:        embedded,
						srcdocDepth: frame.srcdocDepth + 1,
					})
				}
				continue
			}
		}
		if node.Type == xhtml.TextNode {
			rawText := node.Data
			truncated := false
			if len(rawText) > budget.remainingTextBytes {
				rawText = rawText[:budget.remainingTextBytes]
				truncated = true
			}
			budget.remainingTextBytes -= len(rawText)
			text := strings.TrimSpace(rawText)
			if text != "" {
				required := len(text)
				if len(parts) > 0 {
					required++
				}
				if textBytes+required > maxBytes {
					break
				}
				parts = append(parts, text)
				textBytes += required
			}
			if truncated {
				break
			}
		}
		for child := node.LastChild; child != nil; child = child.PrevSibling {
			stack = append(stack, accountHealthHTMLTextFrame{
				node:        child,
				srcdocDepth: frame.srcdocDepth,
			})
		}
	}
	return normalizeAccountHealthText(strings.Join(parts, "\n"))
}

func accountHealthHTMLWithinNodeLimit(reader *strings.Reader, maxNodes int) bool {
	if maxNodes <= 0 {
		return false
	}
	tokenizer := xhtml.NewTokenizer(reader)
	nodes := 0
	activeFormatting := 0
	formattingCounts := make(map[string]int)
	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case xhtml.ErrorToken:
			// Let xhtml.Parse retain responsibility for reporting malformed HTML.
			return true
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			nodes++
			if nodes > maxNodes {
				return false
			}
			tagName, _ := tokenizer.TagName()
			tag := string(tagName)
			if accountHealthIsFormattingTag(tag) {
				formattingCounts[tag]++
				activeFormatting++
				if activeFormatting > accountHealthMaxActiveFormatting {
					return false
				}
			}
		case xhtml.EndTagToken:
			tagName, _ := tokenizer.TagName()
			tag := string(tagName)
			if formattingCounts[tag] > 0 {
				formattingCounts[tag]--
				activeFormatting--
			}
		case xhtml.TextToken, xhtml.CommentToken, xhtml.DoctypeToken:
			nodes++
			if nodes > maxNodes {
				return false
			}
		}
	}
}

func accountHealthIsFormattingTag(tag string) bool {
	switch tag {
	case "a", "b", "big", "code", "em", "font", "i", "nobr", "s", "small", "strike", "strong", "tt", "u":
		return true
	default:
		return false
	}
}

func normalizeAccountHealthText(value string) string {
	value = stdhtml.UnescapeString(value)
	value = strings.Map(func(char rune) rune {
		switch {
		case char == '\u3000':
			return ' '
		case char >= '\uff01' && char <= '\uff5e':
			return char - 0xfee0
		default:
			return char
		}
	}, value)
	value = strings.ReplaceAll(value, "\ufeff", "")
	value = strings.ReplaceAll(value, "\u200b", "")
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if normalized := strings.Join(strings.Fields(line), " "); normalized != "" {
			result = append(result, normalized)
		}
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
}

type accountHealthHTMLScan struct {
	articles               []*xhtml.Node
	messageContainers      []*xhtml.Node
	articleMessageOverlaps map[*xhtml.Node]struct{}
	headings               []*xhtml.Node
	links                  []*xhtml.Node
	dynamicHint            bool
}

type accountHealthHTMLScanFrame struct {
	node                 *xhtml.Node
	articleRoot          *xhtml.Node
	messageContainerRoot *xhtml.Node
}

func accountHealthScanHTML(root *xhtml.Node, budget *accountHealthHTMLTextBudget) accountHealthHTMLScan {
	scan := accountHealthHTMLScan{
		articleMessageOverlaps: make(map[*xhtml.Node]struct{}),
	}
	if root == nil || budget == nil {
		return scan
	}
	stack := []accountHealthHTMLScanFrame{{node: root}}
	for len(stack) > 0 && budget.takeNode() {
		frame := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		node := frame.node
		isArticle := node.Type == xhtml.ElementNode && node.Data == "article"
		isMessageContainer := accountHealthIsMessageContainer(node)
		articleRoot := frame.articleRoot
		messageContainerRoot := frame.messageContainerRoot

		if node.Type == xhtml.ElementNode {
			switch node.Data {
			case "script":
				scan.dynamicHint = true
			case "a":
				if _, ok := accountHealthHTMLAttribute(node, "href"); ok {
					scan.links = append(scan.links, node)
				}
			case "h1", "h2", "h3", "h4":
				scan.headings = append(scan.headings, node)
			}
			if isArticle && articleRoot == nil {
				scan.articles = append(scan.articles, node)
				articleRoot = node
			}
			if isMessageContainer && messageContainerRoot == nil {
				scan.messageContainers = append(scan.messageContainers, node)
				messageContainerRoot = node
			}
			if articleRoot != nil && messageContainerRoot != nil {
				scan.articleMessageOverlaps[articleRoot] = struct{}{}
			}
		}

		for child := node.LastChild; child != nil; child = child.PrevSibling {
			stack = append(stack, accountHealthHTMLScanFrame{
				node:                 child,
				articleRoot:          articleRoot,
				messageContainerRoot: messageContainerRoot,
			})
		}
	}
	return scan
}

func accountHealthIsMessageContainer(node *xhtml.Node) bool {
	if node == nil || node.Type != xhtml.ElementNode {
		return false
	}
	if _, ok := accountHealthHTMLAttribute(node, "data-message-id"); ok {
		return true
	}
	className, ok := accountHealthHTMLAttribute(node, "class")
	if !ok {
		return false
	}
	className = strings.ToLower(strings.ReplaceAll(className, "_", "-"))
	for _, token := range strings.Fields(className) {
		for _, marker := range []string{"mail-item", "message-item", "email-item", "mail-card", "message-card"} {
			markerIndex := strings.Index(token, marker)
			if markerIndex < 0 {
				continue
			}
			suffix := token[markerIndex+len(marker):]
			if !accountHealthIsMessageContainerChromeSuffix(suffix) {
				return true
			}
		}
	}
	return false
}

func accountHealthIsMessageContainerChromeSuffix(suffix string) bool {
	for _, prefix := range []string{
		"-list", "-toolbar", "-header", "-footer", "-actions", "-controls",
		"-button", "-icon", "-badge", "-meta", "-title", "-subject",
	} {
		if strings.HasPrefix(suffix, prefix) {
			return true
		}
	}
	return false
}

func accountHealthHeadingBlocks(
	headings []*xhtml.Node,
	limit int,
	fingerprints map[string]struct{},
	budget *accountHealthHTMLTextBudget,
) ([]string, []*xhtml.Node) {
	blocks := make([]string, 0, limit)
	roots := make([]*xhtml.Node, 0, limit)
	textCache := make(map[*xhtml.Node]string)
	for _, heading := range headings {
		if len(blocks) >= limit || budget == nil || budget.remainingNodes <= 0 || budget.remainingTextBytes <= 0 {
			break
		}
		headingText := accountHealthVisibleTextWithBudget(heading, budget, 1_000)
		if headingText == "" {
			continue
		}
		chosen := ""
		var chosenRoot *xhtml.Node
		parent := heading
		for depth := 0; depth < 5 && parent.Parent != nil; depth++ {
			parent = parent.Parent
			if parent.Type == xhtml.ElementNode && (parent.Data == "body" || parent.Data == "html") {
				break
			}
			text, cached := textCache[parent]
			if !cached {
				text = accountHealthVisibleTextWithBudget(parent, budget, 30_001)
				textCache[parent] = text
			}
			if len(text) > 30_000 {
				break
			}
			if len(text) < 60 {
				continue
			}
			chosen = text
			chosenRoot = parent
			if len(text) >= len(headingText)+80 {
				break
			}
		}
		if chosen != "" {
			blocks = appendUniqueAccountHealthBlock(blocks, fingerprints, chosen)
			roots = append(roots, chosenRoot)
		}
	}
	return blocks, roots
}

func accountHealthLinksIntersectingMessageRoots(roots []*xhtml.Node) map[*xhtml.Node]struct{} {
	links := make(map[*xhtml.Node]struct{})
	descendantSeen := make(map[*xhtml.Node]struct{})
	stack := append([]*xhtml.Node(nil), roots...)
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if node == nil {
			continue
		}
		if _, seen := descendantSeen[node]; seen {
			continue
		}
		descendantSeen[node] = struct{}{}
		if node.Type == xhtml.ElementNode && node.Data == "a" {
			links[node] = struct{}{}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			stack = append(stack, child)
		}
	}

	ancestorSeen := make(map[*xhtml.Node]struct{})
	for _, root := range roots {
		for node := root; node != nil; node = node.Parent {
			if _, seen := ancestorSeen[node]; seen {
				break
			}
			ancestorSeen[node] = struct{}{}
			if node.Type == xhtml.ElementNode && node.Data == "a" {
				links[node] = struct{}{}
			}
		}
	}
	return links
}

func accountHealthHTMLAttribute(node *xhtml.Node, key string) (string, bool) {
	if node == nil {
		return "", false
	}
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, key) {
			return attribute.Val, true
		}
	}
	return "", false
}

func accountHealthIsNextLink(node *xhtml.Node, budget *accountHealthHTMLTextBudget) bool {
	rel, _ := accountHealthHTMLAttribute(node, "rel")
	for _, token := range strings.Fields(strings.ToLower(rel)) {
		if token == "next" {
			return true
		}
	}
	label := strings.ToLower(accountHealthVisibleTextWithLimits(node, budget, 256, accountHealthMaxLinkTextNodes))
	switch label {
	case "next", "next page", "下一页", "下一頁", "次へ", "suivant", "siguiente":
		return true
	default:
		return false
	}
}

func appendUniqueAccountHealthBlock(
	blocks []string,
	fingerprints map[string]struct{},
	value string,
) []string {
	fingerprint := strings.ToLower(strings.Join(strings.Fields(value), " "))
	if fingerprint == "" {
		return blocks
	}
	if _, duplicate := fingerprints[fingerprint]; duplicate {
		return blocks
	}
	fingerprints[fingerprint] = struct{}{}
	return append(blocks, value)
}

func classifyAccountHealthMessages(blocks []string, expectedEmail string, pages int, dynamicHint bool, now time.Time) accountHealthDetection {
	if pages < 0 {
		pages = 0
	}
	plus := chooseAccountHealthPlusEvidence(blocks, expectedEmail)
	detections := make([]accountHealthDetection, 0, len(blocks))
	for _, block := range blocks {
		detections = append(detections, classifyAccountHealthMessage(block))
	}

	meaningful := make([]accountHealthDetection, 0, len(detections))
	for _, detection := range detections {
		if detection.Status != accountHealthStatusNotFound {
			meaningful = append(meaningful, detection)
		}
	}
	if len(meaningful) > 0 {
		matching := make([]accountHealthDetection, 0, len(meaningful))
		unscoped := make([]accountHealthDetection, 0, len(meaningful))
		otherAccounts := make([]accountHealthDetection, 0, len(meaningful))
		for _, detection := range meaningful {
			switch {
			case detection.AssociatedEmail == "":
				unscoped = append(unscoped, detection)
			case strings.EqualFold(detection.AssociatedEmail, expectedEmail):
				matching = append(matching, detection)
			default:
				otherAccounts = append(otherAccounts, detection)
			}
		}
		var compatible []accountHealthDetection
		banScope := matching
		if len(matching) > 0 {
			compatible = append([]accountHealthDetection(nil), matching...)
			for _, detection := range unscoped {
				if detection.Status == accountHealthStatusReactivated {
					compatible = append(compatible, detection)
				}
			}
		} else if len(unscoped) > 0 {
			compatible = unscoped
			banScope = unscoped
		} else {
			compatible = otherAccounts
		}
		validBans := make([]accountHealthDetection, 0, len(banScope))
		for _, detection := range banScope {
			if detection.Status == accountHealthStatusDeactivated && !accountHealthDateKey(detection.MessageDate).IsZero() {
				validBans = append(validBans, detection)
			}
		}
		banDate := accountHealthChooseBanDate(validBans, plus.date)
		chosen := chooseAccountHealthLifecycleDetection(compatible)
		accountSpecific := chosen.Status == accountHealthStatusDeactivated ||
			chosen.Status == accountHealthStatusReactivated ||
			chosen.Status == accountHealthStatusWarning
		if accountSpecific && chosen.AssociatedEmail != "" && !strings.EqualFold(chosen.AssociatedEmail, expectedEmail) {
			chosen.Status = accountHealthStatusMismatch
			chosen.Evidence = append(chosen.Evidence, accountHealthEvidenceAccountMismatch)
		}
		if chosen.Status == accountHealthStatusDeactivated && plus.detected &&
			accountHealthCompareDates(plus.latestDate, chosen.MessageDate) == accountHealthDateOrderAfter {
			chosen.Status = accountHealthStatusReactivated
			if chosen.Score < 90 {
				chosen.Score = 90
			}
			chosen.Evidence = append(chosen.Evidence, accountHealthEvidencePlusAfterBan)
		}
		chosen.MessagesScanned = len(blocks)
		chosen.PagesScanned = pages
		return attachAccountHealthPlus(chosen, plus, banDate, now)
	}

	if len(blocks) == 0 || (dynamicHint && accountHealthBlocksTooShort(blocks)) {
		return attachAccountHealthPlus(accountHealthDetection{
			Status:          accountHealthStatusParseError,
			Evidence:        []string{accountHealthEvidenceDynamicMailbox},
			MessagesScanned: len(blocks),
			PagesScanned:    pages,
		}, plus, "", now)
	}
	return attachAccountHealthPlus(accountHealthDetection{
		Status:          accountHealthStatusNotFound,
		MessagesScanned: len(blocks),
		PagesScanned:    pages,
	}, plus, "", now)
}

func classifyAccountHealthMessage(block string) accountHealthDetection {
	text := normalizeAccountHealthText(block)
	subject := accountHealthSubject(text)
	messageDate := accountHealthMessageDate(text)
	associatedEmail := accountHealthAssociatedEmail(text)
	base := accountHealthDetection{
		Status:          accountHealthStatusNotFound,
		Subject:         subject,
		MessageDate:     messageDate,
		AssociatedEmail: associatedEmail,
	}
	if !strings.Contains(strings.ToLower(text), "openai") {
		return base
	}

	if accountHealthMatchesAny(text, accountHealthReactivatedRules) {
		return accountHealthDetection{
			Status:          accountHealthStatusReactivated,
			Score:           90,
			Evidence:        []string{accountHealthEvidenceReactivated},
			Subject:         subject,
			MessageDate:     messageDate,
			AssociatedEmail: associatedEmail,
		}
	}

	best := base
	for _, rules := range accountHealthLanguageRules {
		if !accountHealthMatchesAny(text, rules.primary) {
			continue
		}
		score := 55
		evidence := []string{accountHealthEvidenceDeactivated}
		if strings.Contains(strings.ToLower(text), "openai") {
			score += 10
			evidence = append(evidence, accountHealthEvidenceOpenAI)
		}
		if accountHealthMatchesAny(text, rules.consequence) {
			score += 20
			evidence = append(evidence, accountHealthEvidenceAccountUnavailable)
		}
		if accountHealthMatchesAny(text, rules.policy) {
			score += 15
			evidence = append(evidence, accountHealthEvidencePolicyViolation)
		}
		if accountHealthMatchesAny(text, rules.appeal) {
			score += 15
			evidence = append(evidence, accountHealthEvidenceAppeal)
		}
		if score > 100 {
			score = 100
		}
		if score > best.Score {
			status := accountHealthStatusNotFound
			if score >= 75 {
				status = accountHealthStatusDeactivated
			}
			best = accountHealthDetection{
				Status:          status,
				Score:           score,
				Language:        rules.name,
				Evidence:        evidence,
				Subject:         subject,
				MessageDate:     messageDate,
				AssociatedEmail: associatedEmail,
			}
		}
	}
	if best.Status == accountHealthStatusDeactivated {
		return best
	}
	if accountHealthMatchesAny(subject, accountHealthExactSubjectRules) {
		return accountHealthDetection{
			Status:          accountHealthStatusDeactivated,
			Score:           100,
			Language:        "模板",
			Evidence:        []string{accountHealthEvidenceDeactivationSubject},
			Subject:         subject,
			MessageDate:     messageDate,
			AssociatedEmail: associatedEmail,
		}
	}
	if accountHealthMatchesAny(text, accountHealthWarningRules) {
		return accountHealthDetection{
			Status:          accountHealthStatusWarning,
			Score:           75,
			Evidence:        []string{accountHealthEvidenceWarning},
			Subject:         subject,
			MessageDate:     messageDate,
			AssociatedEmail: associatedEmail,
		}
	}
	return best
}

func detectAccountHealthPlus(block string) accountHealthPlusEvidence {
	text := normalizeAccountHealthText(block)
	if !accountHealthPlusPattern.MatchString(text) ||
		accountHealthMatchesAny(text, accountHealthPlusCancellationRules) ||
		accountHealthMatchesAny(text, accountHealthPlusFailureRules) {
		return accountHealthPlusEvidence{}
	}
	score := 20
	evidence := []string{accountHealthEvidencePlus}
	language := "模板"
	confirmed := false
	for _, rule := range accountHealthPlusLanguageRules {
		if accountHealthMatchesAny(text, rule.patterns) {
			score += 50
			language = rule.name
			confirmed = true
			evidence = append(evidence, accountHealthEvidenceSubscriptionSuccess)
			break
		}
	}
	if accountHealthOrderPattern.MatchString(text) {
		score += 25
		evidence = append(evidence, accountHealthEvidenceOrderNumber)
	}
	paymentMethod := accountHealthPaymentMethod(text)
	if paymentMethod != "" {
		score += 15
		evidence = append(evidence, accountHealthEvidencePaymentMethod)
	}
	if accountHealthContainsLabel(text, accountHealthPlusDateLabels) {
		score += 10
		evidence = append(evidence, accountHealthEvidenceOrderDate)
	}
	if accountHealthMatchesAny(text, accountHealthSubscriptionManagementRules) {
		score += 10
		evidence = append(evidence, accountHealthEvidenceSubscriptionManagement)
	}
	if score > 100 {
		score = 100
	}
	mailDate := accountHealthMessageDate(text)
	orderDate := accountHealthLabeledDate(text, accountHealthPlusDateLabels)
	plusDate := mailDate
	if !strings.Contains(mailDate, " ") {
		if orderDate != "" {
			plusDate = orderDate
		}
	}
	return accountHealthPlusEvidence{
		detected:        confirmed && score >= 70,
		score:           score,
		language:        language,
		date:            plusDate,
		paymentMethod:   paymentMethod,
		associatedEmail: accountHealthAssociatedEmail(text),
		evidence:        evidence,
	}
}

func chooseAccountHealthPlusEvidence(blocks []string, expectedEmail string) accountHealthPlusEvidence {
	matching := make([]accountHealthPlusEvidence, 0)
	unscoped := make([]accountHealthPlusEvidence, 0)
	for _, block := range blocks {
		candidate := detectAccountHealthPlus(block)
		if !candidate.detected {
			continue
		}
		if candidate.associatedEmail == "" {
			unscoped = append(unscoped, candidate)
		} else if strings.EqualFold(candidate.associatedEmail, expectedEmail) {
			matching = append(matching, candidate)
		}
	}
	candidates := matching
	if len(candidates) == 0 {
		candidates = unscoped
	}
	if len(candidates) == 0 {
		return accountHealthPlusEvidence{}
	}
	chosen := candidates[0]
	hasDated := false
	var latestDate string
	for _, candidate := range candidates {
		candidateDate := accountHealthDateKey(candidate.date)
		if candidateDate.IsZero() {
			if !hasDated && candidate.score > chosen.score {
				chosen = candidate
			}
			continue
		}
		chosenDate := accountHealthDateKey(chosen.date)
		if !hasDated || chosenDate.IsZero() || accountHealthCompareDates(candidate.date, chosen.date) == accountHealthDateOrderBefore {
			chosen = candidate
			hasDated = true
		}
		if latestDate == "" || accountHealthCompareDates(candidate.date, latestDate) == accountHealthDateOrderAfter {
			latestDate = candidate.date
		}
	}
	chosen.latestDate = latestDate
	if chosen.paymentMethod == "" {
		for _, candidate := range candidates {
			if candidate.paymentMethod != "" {
				chosen.paymentMethod = candidate.paymentMethod
				break
			}
		}
	}
	return chosen
}

func chooseAccountHealthLifecycleDetection(detections []accountHealthDetection) accountHealthDetection {
	priority := map[accountHealthDetectionStatus]int{
		accountHealthStatusDeactivated: 4,
		accountHealthStatusMismatch:    3,
		accountHealthStatusWarning:     2,
		accountHealthStatusReactivated: 1,
	}
	allLifecycleDated := true
	hasLifecycle := false
	var latest accountHealthDetection
	var latestDate time.Time
	for _, detection := range detections {
		if detection.Status != accountHealthStatusDeactivated && detection.Status != accountHealthStatusReactivated {
			continue
		}
		hasLifecycle = true
		date := accountHealthDateKey(detection.MessageDate)
		if date.IsZero() {
			allLifecycleDated = false
			continue
		}
		order := accountHealthCompareDates(detection.MessageDate, latest.MessageDate)
		if latestDate.IsZero() || order == accountHealthDateOrderAfter ||
			((order == accountHealthDateOrderEqual || order == accountHealthDateOrderUnknown) && priority[detection.Status] > priority[latest.Status]) {
			latestDate = date
			latest = detection
		}
	}
	if hasLifecycle && allLifecycleDated {
		return latest
	}
	chosen := detections[0]
	for _, detection := range detections[1:] {
		if priority[detection.Status] > priority[chosen.Status] || (priority[detection.Status] == priority[chosen.Status] && detection.Score > chosen.Score) {
			chosen = detection
		}
	}
	return chosen
}

func accountHealthChooseBanDate(bans []accountHealthDetection, plusDate string) string {
	if len(bans) == 0 {
		return ""
	}
	plusAt := accountHealthDateKey(plusDate)
	pool := bans
	if !plusAt.IsZero() {
		postPlus := make([]accountHealthDetection, 0, len(bans))
		for _, ban := range bans {
			if date := accountHealthDateKey(ban.MessageDate); !date.IsZero() &&
				accountHealthCompareDates(ban.MessageDate, plusDate) != accountHealthDateOrderBefore {
				postPlus = append(postPlus, ban)
			}
		}
		if len(postPlus) > 0 {
			pool = postPlus
		}
	}
	chosen := pool[0]
	for _, ban := range pool[1:] {
		if accountHealthCompareDates(ban.MessageDate, chosen.MessageDate) == accountHealthDateOrderBefore {
			chosen = ban
		}
	}
	return chosen.MessageDate
}

func attachAccountHealthPlus(detection accountHealthDetection, plus accountHealthPlusEvidence, banDate string, now time.Time) accountHealthDetection {
	detection.PlusDetected = plus.detected
	detection.PlusScore = plus.score
	detection.PlusLanguage = plus.language
	detection.PlusDate = plus.date
	detection.PaymentMethod = plus.paymentMethod
	detection.PlusEvidence = append([]string(nil), plus.evidence...)
	detection.BanDate = banDate
	if detection.BanDate == "" && detection.Status == accountHealthStatusDeactivated {
		detection.BanDate = detection.MessageDate
	}
	if !plus.detected {
		detection.LifespanStatus = accountHealthLifespanUnavailable
		detection.LifespanSeconds = nil
		return detection
	}
	plusAt := accountHealthDateKey(plus.date)
	banAt := accountHealthDateKey(detection.BanDate)
	banOrder := accountHealthCompareDates(detection.BanDate, plus.date)
	switch {
	case !banAt.IsZero() && !plusAt.IsZero() && banOrder == accountHealthDateOrderBefore:
		detection.LifespanStatus, detection.LifespanSeconds = accountHealthLifespan(plus.date, "", now)
	case !banAt.IsZero() && !plusAt.IsZero() && banOrder == accountHealthDateOrderUnknown:
		detection.LifespanStatus = accountHealthLifespanInvalidDate
		detection.LifespanSeconds = nil
	case detection.BanDate != "":
		detection.LifespanStatus, detection.LifespanSeconds = accountHealthLifespan(plus.date, detection.BanDate, now)
	case detection.Status == accountHealthStatusDeactivated:
		detection.LifespanStatus = accountHealthLifespanBanDateUnknown
		detection.LifespanSeconds = nil
	default:
		detection.LifespanStatus, detection.LifespanSeconds = accountHealthLifespan(plus.date, "", now)
	}
	return detection
}

func accountHealthLifespan(plusDate, banDate string, now time.Time) (accountHealthLifespanStatus, *int64) {
	start := accountHealthDateKey(plusDate)
	if start.IsZero() {
		return accountHealthLifespanUnavailable, nil
	}
	end := now
	status := accountHealthLifespanActive
	if banDate != "" {
		end = accountHealthDateKey(banDate)
		status = accountHealthLifespanEnded
		if end.IsZero() {
			return accountHealthLifespanUnavailable, nil
		}
	} else if end.IsZero() {
		return accountHealthLifespanUnavailable, nil
	} else {
		end = time.Date(end.Year(), end.Month(), end.Day(), end.Hour(), end.Minute(), end.Second(), 0, time.UTC)
	}
	seconds := int64(end.Sub(start).Seconds())
	if seconds < 0 {
		return accountHealthLifespanInvalidDate, nil
	}
	return status, &seconds
}

func accountHealthSubject(block string) string {
	lines := strings.Split(block, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		clean := accountHealthSubjectLabel.ReplaceAllString(trimmed, "")
		if clean == "" {
			return ""
		}
		return truncateAccountHealthText(clean, 180)
	}
	return ""
}

func accountHealthAssociatedEmail(block string) string {
	for _, pattern := range accountHealthAssociatedEmailRules {
		if match := pattern.FindStringSubmatch(block); len(match) == 2 {
			return strings.ToLower(match[1])
		}
	}
	return ""
}

func accountHealthPaymentMethod(text string) string {
	value := accountHealthLabeledValue(text, accountHealthPaymentLabels)
	value = strings.Trim(value, " *_`|：:")
	if location := accountHealthPaymentStop.FindStringIndex(value); location != nil {
		value = strings.TrimSpace(value[:location[0]])
	}
	return normalizeAccountHealthPaymentMethod(value)
}

var accountHealthMaskedCardPattern = regexp.MustCompile(`(?i)^(visa|master\s*card|american\s+express|amex|discover|jcb|unionpay|diners\s+club)(?:\s+card)?(?:\s+(?:ending\s+in|ends\s+in|last\s+four(?:\s+digits)?)\s*|\s*[*x•·-]{2,}\s*)(\d{4})$`)

var accountHealthSafePaymentMethods = map[string]string{
	"alipay":           "Alipay",
	"amex":             "Amex",
	"american express": "American Express",
	"apple pay":        "Apple Pay",
	"card":             "Card",
	"carta":            "Carta",
	"carte":            "Carte",
	"discover":         "Discover",
	"diners club":      "Diners Club",
	"google pay":       "Google Pay",
	"jcb":              "JCB",
	"karte":            "Karte",
	"master card":      "Mastercard",
	"mastercard":       "Mastercard",
	"paypal":           "PayPal",
	"pix":              "Pix",
	"tarjeta":          "Tarjeta",
	"unionpay":         "UnionPay",
	"upi":              "UPI",
	"visa":             "Visa",
	"카드":               "카드",
	"支付宝":              "支付宝",
}

func normalizeAccountHealthPaymentMethod(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 60 {
		return ""
	}
	compact := strings.ToLower(strings.Join(strings.Fields(value), " "))
	if safe, ok := accountHealthSafePaymentMethods[compact]; ok {
		return safe
	}
	match := accountHealthMaskedCardPattern.FindStringSubmatch(value)
	if len(match) != 3 {
		return ""
	}
	brand := strings.ToLower(strings.Join(strings.Fields(match[1]), " "))
	if safe, ok := accountHealthSafePaymentMethods[brand]; ok {
		return safe + " ending in " + match[2]
	}
	return ""
}

func accountHealthLabeledDate(text string, labels []string) string {
	return accountHealthFormatDate(accountHealthParseDate(accountHealthLabeledValue(text, labels)))
}

var accountHealthLabeledValuePatternCache sync.Map

func accountHealthLabeledValue(text string, labels []string) string {
	lines := strings.SplitN(text, "\n", accountHealthMaxLabeledLines+1)
	if len(lines) > accountHealthMaxLabeledLines {
		lines = lines[:accountHealthMaxLabeledLines]
	}
	patterns := accountHealthLabelPatterns(
		labels,
		&accountHealthLabeledValuePatternCache,
		func(label string) string {
			return `(?i)^[ \t]*[*_` + "`" + `]*[ \t]*(?:` + label + `)[ \t]*[*_` + "`" + `]*[ \t]*[:：]?[ \t]*[*_` + "`" + `]*[ \t]*(.*)$`
		},
	)
	for index, line := range lines {
		for _, pattern := range patterns {
			match := pattern.FindStringSubmatch(line)
			if len(match) != 2 {
				continue
			}
			value := strings.TrimSpace(match[1])
			if value == "" {
				for next := index + 1; next < len(lines); next++ {
					if value = strings.TrimSpace(lines[next]); value != "" {
						break
					}
				}
			}
			return value
		}
	}
	return ""
}

var accountHealthContainsLabelPatternCache sync.Map

func accountHealthContainsLabel(text string, labels []string) bool {
	patterns := accountHealthLabelPatterns(
		labels,
		&accountHealthContainsLabelPatternCache,
		func(label string) string { return `(?i)(?:` + label + `)` },
	)
	for _, pattern := range patterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

func accountHealthLabelPatterns(
	labels []string,
	cache *sync.Map,
	expression func(string) string,
) []*regexp.Regexp {
	patterns := make([]*regexp.Regexp, 0, len(labels))
	for _, label := range labels {
		if cached, ok := cache.Load(label); ok {
			if pattern, typeOK := cached.(*regexp.Regexp); typeOK {
				patterns = append(patterns, pattern)
				continue
			}
		}
		compiled := regexp.MustCompile(expression(label))
		actual, _ := cache.LoadOrStore(label, compiled)
		if pattern, ok := actual.(*regexp.Regexp); ok {
			patterns = append(patterns, pattern)
			continue
		}
		patterns = append(patterns, compiled)
	}
	return patterns
}

func accountHealthMessageDate(text string) string {
	return accountHealthFormatDate(accountHealthParseDate(text))
}

func accountHealthParseDate(value string) (time.Time, bool) {
	type datePattern struct {
		pattern *regexp.Regexp
		parse   func([]string) (time.Time, bool)
	}
	patterns := []datePattern{
		{
			pattern: accountHealthNumericDatePattern,
			parse: func(match []string) (time.Time, bool) {
				return accountHealthDateFromParts(match[1], match[2], match[3], match[4], match[5], match[6], match[7], match[8])
			},
		},
		{
			pattern: accountHealthMonthFirstPattern,
			parse: func(match []string) (time.Time, bool) {
				month, ok := accountHealthMonthNumbers[strings.ToLower(match[1])]
				if !ok {
					return time.Time{}, false
				}
				return accountHealthDateFromParts(match[3], fmt.Sprint(int(month)), match[2], match[4], match[5], match[6], match[7], match[8])
			},
		},
		{
			pattern: accountHealthDayFirstPattern,
			parse: func(match []string) (time.Time, bool) {
				month, ok := accountHealthMonthNumbers[strings.ToLower(match[2])]
				if !ok {
					return time.Time{}, false
				}
				return accountHealthDateFromParts(match[3], fmt.Sprint(int(month)), match[1], match[4], match[5], match[6], match[7], match[8])
			},
		},
	}

	chosenStart := -1
	var chosen time.Time
	chosenHasTime := false
	for _, candidatePattern := range patterns {
		for _, indexes := range candidatePattern.pattern.FindAllStringSubmatchIndex(value, -1) {
			match := accountHealthDateSubmatches(value, indexes)
			if len(match) != 9 {
				continue
			}
			parsed, hasTime := candidatePattern.parse(match)
			if parsed.IsZero() || (chosenStart >= 0 && indexes[0] >= chosenStart) {
				continue
			}
			chosenStart = indexes[0]
			chosen = parsed
			chosenHasTime = hasTime
		}
	}
	return chosen, chosenHasTime
}

func accountHealthDateSubmatches(value string, indexes []int) []string {
	if len(indexes)%2 != 0 {
		return nil
	}
	match := make([]string, len(indexes)/2)
	for index := range match {
		start := indexes[index*2]
		end := indexes[index*2+1]
		if start >= 0 && end >= start && end <= len(value) {
			match[index] = value[start:end]
		}
	}
	return match
}

func accountHealthDateFromParts(yearText, monthText, dayText, hourText, minuteText, secondText, fractionText, timezoneText string) (time.Time, bool) {
	year, err := strconv.Atoi(yearText)
	if err != nil {
		return time.Time{}, false
	}
	month, err := strconv.Atoi(monthText)
	if err != nil {
		return time.Time{}, false
	}
	day, err := strconv.Atoi(dayText)
	if err != nil {
		return time.Time{}, false
	}
	hour, minute, second, nanosecond := 0, 0, 0, 0
	hasTime := hourText != ""
	if !hasTime && timezoneText != "" {
		return time.Time{}, false
	}
	if hasTime {
		hour, err = strconv.Atoi(hourText)
		if err != nil {
			return time.Time{}, false
		}
		minute, err = strconv.Atoi(minuteText)
		if err != nil {
			return time.Time{}, false
		}
		if secondText != "" {
			second, err = strconv.Atoi(secondText)
			if err != nil {
				return time.Time{}, false
			}
		}
		if fractionText != "" {
			fractionText += strings.Repeat("0", 9-len(fractionText))
			nanosecond, err = strconv.Atoi(fractionText)
			if err != nil {
				return time.Time{}, false
			}
		}
	}
	location, ok := accountHealthTimezoneLocation(timezoneText)
	if !ok {
		return time.Time{}, false
	}
	parsed := time.Date(year, time.Month(month), day, hour, minute, second, nanosecond, location)
	if parsed.Year() != year || int(parsed.Month()) != month || parsed.Day() != day || parsed.Hour() != hour || parsed.Minute() != minute || parsed.Second() != second {
		return time.Time{}, false
	}
	return parsed.UTC(), hasTime
}

func accountHealthTimezoneLocation(value string) (*time.Location, bool) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" || value == "Z" || value == "UTC" || value == "GMT" {
		return time.UTC, true
	}
	if strings.HasPrefix(value, "UTC") || strings.HasPrefix(value, "GMT") {
		value = value[3:]
	}
	if len(value) != 5 && len(value) != 6 {
		return nil, false
	}
	sign := 1
	switch value[0] {
	case '-':
		sign = -1
	case '+':
	default:
		return nil, false
	}
	digits := strings.ReplaceAll(value[1:], ":", "")
	if len(digits) != 4 {
		return nil, false
	}
	hours, hourErr := strconv.Atoi(digits[:2])
	minutes, minuteErr := strconv.Atoi(digits[2:])
	if hourErr != nil || minuteErr != nil || hours > 14 || minutes > 59 || (hours == 14 && minutes != 0) {
		return nil, false
	}
	offset := sign * ((hours * 60 * 60) + (minutes * 60))
	return time.FixedZone(value, offset), true
}

func accountHealthFormatDate(value time.Time, hasTime bool) string {
	if value.IsZero() {
		return ""
	}
	if hasTime {
		return value.Format("2006-01-02 15:04:05")
	}
	return value.Format("2006-01-02")
}

func accountHealthDateKey(value string) time.Time {
	parsed, _ := accountHealthDateKeyWithPrecision(value)
	return parsed
}

func accountHealthDateKeyWithPrecision(value string) (time.Time, bool) {
	if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", value, time.UTC); err == nil {
		return parsed, true
	}
	if parsed, err := time.ParseInLocation("2006-01-02", value, time.UTC); err == nil {
		return parsed, false
	}
	return time.Time{}, false
}

func accountHealthCompareDates(left, right string) accountHealthDateOrder {
	leftDate, leftHasTime := accountHealthDateKeyWithPrecision(left)
	rightDate, rightHasTime := accountHealthDateKeyWithPrecision(right)
	if leftDate.IsZero() || rightDate.IsZero() {
		return accountHealthDateOrderUnknown
	}
	if leftDate.Year() == rightDate.Year() && leftDate.YearDay() == rightDate.YearDay() && (!leftHasTime || !rightHasTime) {
		return accountHealthDateOrderUnknown
	}
	switch {
	case leftDate.Before(rightDate):
		return accountHealthDateOrderBefore
	case leftDate.After(rightDate):
		return accountHealthDateOrderAfter
	default:
		return accountHealthDateOrderEqual
	}
}

func accountHealthBlocksTooShort(blocks []string) bool {
	for _, block := range blocks {
		if len(strings.TrimSpace(block)) >= 40 {
			return false
		}
	}
	return true
}

func accountHealthMatchesAny(value string, patterns []*regexp.Regexp) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}

func truncateAccountHealthText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
