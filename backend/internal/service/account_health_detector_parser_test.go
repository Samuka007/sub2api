package service

import (
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	accountHealthTestPlus = `
You've successfully subscribed to ChatGPT Plus.
Enjoy your first month free. Your subscription will automatically renew monthly.
Order number: sub_1SyntheticOrder
Order date: Jul 16, 2026
Payment method: UPI
`
	accountHealthTestBan = `
OpenAI - Access Deactivated
We're writing with an important update about your OpenAI account associated with user@example.com.
Your account has been deactivated because recent activity violated our Terms and Usage Policies.
This means your account can no longer be used.
If you believe this decision was made in error, start an appeal.
`
)

func accountHealthOverflowURLForTest(pathToken string) string {
	var query strings.Builder
	for index := range accountHealthMaxURLSecrets {
		if index > 0 {
			_ = query.WriteByte('&')
		}
		_, _ = fmt.Fprintf(&query, "opaque%02d=value%02d", index, index)
	}
	return "https://mail.example/" + pathToken + "?mail=user%40example.com&" + query.String()
}

func TestParseAccountHealthGroupDescription(t *testing.T) {
	tests := []struct {
		name        string
		description string
		wantEmail   string
		wantURL     string
	}{
		{
			name: "mail and SMS export",
			description: "User@Example.com---https://mail.example/open.php?mail=user%40example.com&pwd=synthetic&limit=5" +
				"---+19045550123---https://sms.example/api/system/get_sms/synthetic-sms-token",
			wantEmail: "user@example.com",
			wantURL:   "https://mail.example/open.php?mail=user%40example.com&pwd=synthetic&limit=5",
		},
		{
			name: "four hyphen export compatibility",
			description: "User@Example.com----https://mail.example/open.php?mail=user%40example.com&pwd=synthetic&limit=5" +
				"----+19045550123----https://sms.example/api/system/get_sms/synthetic-sms-token",
			wantEmail: "user@example.com",
			wantURL:   "https://mail.example/open.php?mail=user%40example.com&pwd=synthetic&limit=5",
		},
		{
			name:        "four hyphen two field compatibility",
			description: "reader@example.com----https://mail.example/inbox",
			wantEmail:   "reader@example.com",
			wantURL:     "https://mail.example/inbox",
		},
		{
			name: "formatted international phone",
			description: "reader@example.com---https://mail.example/inbox---+1 (904) 882-9730---" +
				"https://sms.example/messages/synthetic-token",
			wantEmail: "reader@example.com",
			wantURL:   "https://mail.example/inbox",
		},
		{
			name:        "two field export",
			description: "reader@example.com---https://mail.example/open?mail=reader%40example.com&pwd=synthetic",
			wantEmail:   "reader@example.com",
			wantURL:     "https://mail.example/open?mail=reader%40example.com&pwd=synthetic",
		},
		{
			name:        "literal plus email in URL query",
			description: "user+tag@example.com---https://mail.example/open?mail=user+tag%40example.com&pwd=synthetic",
			wantEmail:   "user+tag@example.com",
			wantURL:     "https://mail.example/open?mail=user+tag%40example.com&pwd=synthetic",
		},
		{
			name:        "escaped plus email in URL query",
			description: "user+tag@example.com---https://mail.example/open?mail=user%2Btag%40example.com&pwd=synthetic",
			wantEmail:   "user+tag@example.com",
			wantURL:     "https://mail.example/open?mail=user%2Btag%40example.com&pwd=synthetic",
		},
		{
			name:        "plus email in URL path",
			description: "user+tag@example.com---https://mail.example/inbox/user+tag@example.com/messages",
			wantEmail:   "user+tag@example.com",
			wantURL:     "https://mail.example/inbox/user+tag@example.com/messages",
		},
		{
			name:        "email before URL takes precedence",
			description: "preferred@example.com---https://mail.example/open?mail=other%40example.com&pwd=synthetic",
			wantEmail:   "preferred@example.com",
			wantURL:     "https://mail.example/open?mail=other%40example.com&pwd=synthetic",
		},
		{
			name: "export header and HTML entities",
			description: "卡密导出\nreader@example.com---https://mail.example/open?mail=reader%40example.com&amp;pwd=synthetic" +
				"&amp;limit=5",
			wantEmail: "reader@example.com",
			wantURL:   "https://mail.example/open?mail=reader%40example.com&pwd=synthetic&limit=5",
		},
		{
			name:        "single mailbox URL can use preceding account email",
			description: "reader@example.com---https://mail.example/inbox",
			wantEmail:   "reader@example.com",
			wantURL:     "https://mail.example/inbox",
		},
		{
			name:        "mail URL trailing punctuation is preserved",
			description: "reader@example.com---https://mail.example/inbox/token.",
			wantEmail:   "reader@example.com",
			wantURL:     "https://mail.example/inbox/token.",
		},
		{
			name:        "encoded field separator stays in mail URL",
			description: "reader@example.com---https://mail.example/inbox/token%2D%2D%2Dpart",
			wantEmail:   "reader@example.com",
			wantURL:     "https://mail.example/inbox/token%2D%2D%2Dpart",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target, err := parseAccountHealthMailboxTarget(test.description)
			require.NoError(t, err)
			require.Equal(t, test.wantEmail, target.email)
			require.Equal(t, test.wantURL, target.mailboxURL)
		})
	}
}

func TestParseAccountHealthGroupDescriptionIgnoresTrailingSecrets(t *testing.T) {
	target, err := parseAccountHealthMailboxTarget(
		"owner@example.com---https://mail.example/inbox?mail=owner%40example.com" +
			"---+19045550123---https://sms.example/api/system/get_sms/synthetic-sms-token",
	)

	require.NoError(t, err)
	require.Equal(t, "https://mail.example/inbox?mail=owner%40example.com", target.mailboxURL)
	require.Empty(t, target.secrets)
}

func TestParseAccountHealthGroupDescriptionIgnoresTrailingFields(t *testing.T) {
	const mailboxURL = "https://mail.example/open?mail=user%40example.com&pwd=synthetic"
	tests := []struct {
		name        string
		description string
		wantURL     string
	}{
		{name: "single arbitrary field", description: "user@example.com---" + mailboxURL + "---anything", wantURL: mailboxURL},
		{name: "invalid phone and insecure SMS URL", description: "user@example.com---" + mailboxURL + "---not-a-phone---http://sms.example/messages", wantURL: mailboxURL},
		{name: "five fields", description: "user@example.com---" + mailboxURL + "---+19045550123---https://sms.example/messages---tail", wantURL: mailboxURL},
		{name: "mixed separator after three hyphens", description: "user@example.com---" + mailboxURL + "----+19045550123----https://sms.example/messages", wantURL: mailboxURL},
		{name: "mixed separator after four hyphens", description: "user@example.com----" + mailboxURL + "---+19045550123---https://sms.example/messages", wantURL: mailboxURL},
		{name: "five hyphens begin ignored suffix", description: "user@example.com---" + mailboxURL + "-----tail", wantURL: mailboxURL},
		{name: "second account on same line", description: "user@example.com---" + mailboxURL + "---other@example.com---https://mail.example/other", wantURL: mailboxURL},
		{name: "path-looking suffix", description: "user@example.com---https://mail.example/token---part", wantURL: "https://mail.example/token"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target, err := parseAccountHealthMailboxTarget(test.description)
			require.NoError(t, err)
			require.Equal(t, "user@example.com", target.email)
			require.Equal(t, test.wantURL, target.mailboxURL)
		})
	}
}

func TestParseAccountHealthGroupDescriptionRejectsInvalidFormatsWithoutLeakingInput(t *testing.T) {
	secretURL := "https://mail.example/open?mail=user%40example.com&pwd=do-not-leak"
	tests := []struct {
		name        string
		description string
		wantKind    accountHealthDescriptionErrorKind
	}{
		{name: "empty", description: " \n# ignored", wantKind: accountHealthDescriptionEmpty},
		{name: "missing URL", description: "user@example.com---do-not-leak", wantKind: accountHealthDescriptionMissingURL},
		{name: "missing email", description: "not-an-email---" + secretURL, wantKind: accountHealthDescriptionMissingEmail},
		{name: "legacy credential export", description: "user@example.com---password---token---" + secretURL, wantKind: accountHealthDescriptionMissingURL},
		{name: "URL only", description: secretURL, wantKind: accountHealthDescriptionInvalidURL},
		{name: "five hyphen separator", description: "user@example.com-----" + secretURL, wantKind: accountHealthDescriptionInvalidURL},
		{name: "mail URL with surrounding text", description: "user@example.com---prefix " + secretURL + " suffix", wantKind: accountHealthDescriptionInvalidURL},
		{name: "multiple lines", description: "one@example.com---" + secretURL + "\ntwo@example.com---https://mail.example/two?mail=two%40example.com", wantKind: accountHealthDescriptionMultipleRecords},
		{name: "valid and invalid lines", description: "one@example.com---" + secretURL + "\nbroken@example.com---no-link", wantKind: accountHealthDescriptionMixedInvalid},
		{name: "invalid URL", description: "user@example.com---https://", wantKind: accountHealthDescriptionInvalidURL},
		{name: "insecure URL", description: "user@example.com---http://mail.example/inbox", wantKind: accountHealthDescriptionInvalidURL},
		{name: "consecutive domain dots", description: "user@example..com---https://mail.example/inbox---+19045550123---https://sms.example/messages", wantKind: accountHealthDescriptionMissingEmail},
		{name: "empty domain label", description: "user@.example.com---https://mail.example/inbox---+19045550123---https://sms.example/messages", wantKind: accountHealthDescriptionMissingEmail},
		{name: "leading domain hyphen", description: "user@-example.com---https://mail.example/inbox---+19045550123---https://sms.example/messages", wantKind: accountHealthDescriptionMissingEmail},
		{name: "trailing domain hyphen", description: "user@example-.com---https://mail.example/inbox---+19045550123---https://sms.example/messages", wantKind: accountHealthDescriptionMissingEmail},
		{
			name: "three field insecure mailbox is rejected",
			description: "user@example.com---http://mail.example/inbox?mail=user%40example.com---" +
				"https://docs.example/help",
			wantKind: accountHealthDescriptionInvalidURL,
		},
		{
			name: "three field mailbox without account query is rejected",
			description: "user@example.com---http://mail.example/inbox---" +
				"https://docs.example/help",
			wantKind: accountHealthDescriptionInvalidURL,
		},
		{
			name: "fielded account email cannot come from the mailbox URL",
			description: "not-an-email---https://mail.example/inbox?mail=owner%40example.com" +
				"---+19045550123---https://sms.example/messages",
			wantKind: accountHealthDescriptionMissingEmail,
		},
		{
			name: "insecure mailbox is not replaced by HTTPS SMS URL",
			description: "user@example.com---http://mail.example/inbox---+19045550123---" +
				"https://sms.example/messages",
			wantKind: accountHealthDescriptionInvalidURL,
		},
		{
			name:        "empty mailbox field is not replaced by SMS URL",
			description: "user@example.com--- ---+19045550123---https://sms.example/messages",
			wantKind:    accountHealthDescriptionMissingURL,
		},
		{
			name:        "oversized mailbox URL",
			description: "user@example.com---https://mail.example/" + strings.Repeat("x", accountHealthMaxURLBytes),
			wantKind:    accountHealthDescriptionInvalidURL,
		},
		{
			name:        "redaction secret budget overflow",
			description: "user@example.com---" + accountHealthOverflowURLForTest("reflected-path-token"),
			wantKind:    accountHealthDescriptionInvalidURL,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseAccountHealthMailboxTarget(test.description)
			require.Error(t, err)
			var formatErr *accountHealthDescriptionFormatError
			require.True(t, errors.As(err, &formatErr))
			require.Equal(t, test.wantKind, formatErr.kind)
			require.NotContains(t, err.Error(), "do-not-leak")
			require.NotContains(t, err.Error(), "mail.example")
			require.NotContains(t, err.Error(), test.description)
		})
	}
}

func TestParseAccountHealthGroupDescriptionRejectsAlternateRecordSeparators(t *testing.T) {
	for _, test := range []struct {
		name      string
		separator string
	}{
		{name: "carriage return", separator: "\r"},
		{name: "form feed", separator: "\f"},
		{name: "vertical tab", separator: "\v"},
		{name: "next line", separator: "\u0085"},
		{name: "line separator", separator: "\u2028"},
		{name: "paragraph separator", separator: "\u2029"},
	} {
		t.Run(test.name, func(t *testing.T) {
			description := "one@example.com---https://mail.example/one?mail=one%40example.com" +
				test.separator +
				"two@example.com---https://mail.example/two?mail=two%40example.com"
			_, err := parseAccountHealthMailboxTarget(description)
			require.Error(t, err)
			var formatErr *accountHealthDescriptionFormatError
			require.ErrorAs(t, err, &formatErr)
			require.Equal(t, accountHealthDescriptionMultipleRecords, formatErr.kind)
		})
	}

	_, err := parseAccountHealthMailboxTarget(
		"one@example.com---https://mail.example/one?mail=one%40example.com&#13;broken@example.com---no-link",
	)
	require.Error(t, err)
	var formatErr *accountHealthDescriptionFormatError
	require.ErrorAs(t, err, &formatErr)
	require.Equal(t, accountHealthDescriptionMixedInvalid, formatErr.kind)

	_, err = parseAccountHealthMailboxTarget(
		"one@example.com---https://mail.example/one?mail=one%40example.com&#12;broken@example.com---no-link",
	)
	require.Error(t, err)
	require.ErrorAs(t, err, &formatErr)
	require.Equal(t, accountHealthDescriptionMixedInvalid, formatErr.kind)
}

func TestParseAccountHealthGroupDescriptionIgnoresLongSuffixLinearly(t *testing.T) {
	fields := []string{"user@example.com", "https://mail.example/inbox?mail=user%40example.com"}
	for index := 0; index < 2000; index++ {
		fields = append(fields, "ignored")
	}

	target, err := parseAccountHealthMailboxTarget(strings.Join(fields, "---"))
	require.NoError(t, err)
	require.Equal(t, "user@example.com", target.email)
	require.Equal(t, "https://mail.example/inbox?mail=user%40example.com", target.mailboxURL)
}

func TestParseAccountHealthMailboxPageExtractsHTMLMessagesAndSrcdoc(t *testing.T) {
	pageHTML := `
<html><body>
  <script>window.__messages = [];</script>
  <article class="mail-item"><h2>Verification code</h2><p>Your code is 123456.</p></article>
  <article class="mail-item">
    <h2 class="subject">ChatGPT - Your new plan</h2>
    <div class="meta">Time: 2026-07-16 09:30:00</div>
    <iframe srcdoc="&lt;p&gt;You've successfully subscribed to ChatGPT Plus.&lt;/p&gt;
      &lt;p&gt;Order number: sub_SyntheticIframe&lt;/p&gt;
      &lt;p&gt;Payment method: UPI&lt;/p&gt;"></iframe>
  </article>
  <a rel="next" href="?page=2">Next</a>
</body></html>`

	page, err := parseAccountHealthMailboxPage([]byte(pageHTML), "text/html; charset=utf-8", 20)
	require.NoError(t, err)
	require.Len(t, page.blocks, 2)
	require.True(t, page.dynamicHint)
	require.Equal(t, []string{"?page=2"}, page.nextLinks)
	require.Contains(t, page.blocks[1], "successfully subscribed to ChatGPT Plus")

	detection := classifyAccountHealthMessages(page.blocks, "user@example.com", 1, page.dynamicHint, time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC))
	require.True(t, detection.PlusDetected)
	require.Equal(t, "2026-07-16 09:30:00", detection.PlusDate)
	require.Equal(t, "UPI", detection.PaymentMethod)
	require.Equal(t, 2, detection.MessagesScanned)
}

func TestParseAccountHealthMailboxPagePrefersMessageContainersOverUnrelatedArticles(t *testing.T) {
	page, err := parseAccountHealthMailboxPage([]byte(`<html><body>
<article>Product news with enough unrelated content to look like a standalone article.</article>
<div class="mail-item-row">OpenAI - Access Deactivated
Your OpenAI account has been deactivated and can no longer be used. 2026-07-21.</div>
</body></html>`), "text/html", 20)

	require.NoError(t, err)
	require.Len(t, page.blocks, 1)
	detection := classifyAccountHealthMessages(
		page.blocks,
		"user@example.com",
		1,
		false,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	)
	require.Equal(t, accountHealthStatusDeactivated, detection.Status)
}

func TestParseAccountHealthMailboxPageIgnoresHelpArticlesBesideMessageContainers(t *testing.T) {
	page, err := parseAccountHealthMailboxPage([]byte(`<html><body>
<article>OpenAI help: If your OpenAI account has been deactivated, it can no longer be used.</article>
<div class="mail-item-row">OpenAI verification code: 123456.</div>
</body></html>`), "text/html", 20)

	require.NoError(t, err)
	require.Equal(t, []string{"OpenAI verification code: 123456."}, page.blocks)
	detection := classifyAccountHealthMessages(
		page.blocks,
		"user@example.com",
		1,
		false,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	)
	require.Equal(t, accountHealthStatusNotFound, detection.Status)
}

func TestParseAccountHealthMailboxPageDoesNotTreatMessageChromeAsAContainer(t *testing.T) {
	page, err := parseAccountHealthMailboxPage([]byte(`<html><body>
<article>Your OpenAI account has been deactivated and can no longer be used. 2026-07-21.</article>
<div class="message-card-toolbar">OpenAI verification code: 123456.</div>
</body></html>`), "text/html", 20)

	require.NoError(t, err)
	require.Len(t, page.blocks, 1)
	detection := classifyAccountHealthMessages(
		page.blocks,
		"user@example.com",
		1,
		false,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	)
	require.Equal(t, accountHealthStatusDeactivated, detection.Status)
}

func TestParseAccountHealthMailboxPageBoundsMixedCandidateSelectionWork(t *testing.T) {
	body := `<html><body>` +
		strings.Repeat(`<article>a</article>`, 20_000) +
		strings.Repeat(`<div class="mail-item">m</div>`, 20_000) +
		`</body></html>`

	startedAt := time.Now()
	_, err := parseAccountHealthMailboxPage([]byte(body), "text/html", 1)

	require.NoError(t, err)
	require.Less(t, time.Since(startedAt), 5*time.Second)
}

func TestParseAccountHealthMailboxPageHonorsMessageAndBodyLimits(t *testing.T) {
	pageHTML := `<html><body>
<article>mail one has enough ordinary content to form a complete message block.</article>
<article>mail two has enough ordinary content to form a complete message block.</article>
<article>Your OpenAI account has been deactivated and can no longer be used.</article>
</body></html>`

	page, err := parseAccountHealthMailboxPage([]byte(pageHTML), "text/html", 2)
	require.NoError(t, err)
	require.Len(t, page.blocks, 2)
	detection := classifyAccountHealthMessages(page.blocks, "user@example.com", 1, false, time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC))
	require.Equal(t, accountHealthStatusNotFound, detection.Status)

	_, err = parseAccountHealthMailboxPage(make([]byte, accountHealthMaxMailboxBytes+1), "text/html", 20)
	require.ErrorIs(t, err, errAccountHealthMailboxResponseTooLarge)
}

func TestParseAccountHealthMailboxPageBoundsNestedSrcdoc(t *testing.T) {
	nested := `<p>deep-secret-marker</p>`
	for depth := 0; depth < accountHealthMaxSrcdocDepth+3; depth++ {
		nested = `<iframe srcdoc="` + stdhtml.EscapeString(nested) + `"></iframe>`
	}

	page, err := parseAccountHealthMailboxPage(
		[]byte(`<html><body><article>`+nested+`</article></body></html>`),
		"text/html",
		20,
	)
	require.NoError(t, err)
	for _, block := range page.blocks {
		require.NotContains(t, block, "deep-secret-marker")
	}
}

func TestParseAccountHealthMailboxPageSharesNodeBudgetAcrossDiscoveryAndExtraction(t *testing.T) {
	body := `<html><body>` +
		strings.Repeat(`<i></i>`, accountHealthMaxHTMLNodes-5) +
		`<article>late-node-secret-marker has enough content to form a mailbox message.</article>` +
		`</body></html>`

	page, err := parseAccountHealthMailboxPage([]byte(body), "text/html", 20)
	require.NoError(t, err)
	require.NotContains(t, strings.Join(page.blocks, "\n"), "late-node-secret-marker")
}

func TestParseAccountHealthMailboxPageRejectsActiveFormattingComplexity(t *testing.T) {
	var body strings.Builder
	_, _ = body.WriteString("<html><body>")
	for index := 0; index <= accountHealthMaxActiveFormatting; index++ {
		_, _ = fmt.Fprintf(&body, `<b id="format-%d">`, index)
	}
	_, _ = body.WriteString("<article>late-formatting-secret-marker has enough content to form a mailbox message.</article>")
	_, _ = body.WriteString("</body></html>")

	page, err := parseAccountHealthMailboxPage([]byte(body.String()), "text/html", 20)
	require.NoError(t, err)
	require.NotContains(t, strings.Join(page.blocks, "\n"), "late-formatting-secret-marker")
}

func TestParseAccountHealthMailboxPagePrioritizesMessagesOverLinkLabels(t *testing.T) {
	body := `<html><body><a href="?page=2">` +
		strings.Repeat(`<i></i>`, accountHealthMaxHTMLNodes/2) +
		`</a><a href="?page=3">Next</a>` +
		`<article>message-priority-marker has enough content to form a mailbox message.</article>` +
		`</body></html>`

	page, err := parseAccountHealthMailboxPage([]byte(body), "text/html", 20)
	require.NoError(t, err)
	require.Contains(t, strings.Join(page.blocks, "\n"), "message-priority-marker")
	require.Equal(t, []string{"?page=3"}, page.nextLinks)
}

func TestParseAccountHealthMailboxPageSharesTextBudgetAcrossHeadingFallbacks(t *testing.T) {
	nested := `<div>` + strings.Repeat("x", 30_000) + `<h2>load</h2></div>`
	for depth := 1; depth < 300; depth++ {
		nested = `<div>` + nested + `<h2>load</h2></div>`
	}
	body := `<html><body>` + nested +
		`<section><h2>late-text-secret-marker</h2>` +
		`<p>This late message has enough ordinary content to be selected by heading fallback.</p></section>` +
		`</body></html>`

	page, err := parseAccountHealthMailboxPage([]byte(body), "text/html", 20)
	require.NoError(t, err)
	require.NotContains(t, strings.Join(page.blocks, "\n"), "late-text-secret-marker")
}

func TestParseAccountHealthMailboxPageSharesSrcdocByteBudgetAcrossConsumers(t *testing.T) {
	firstSrcdoc := `<!--` + strings.Repeat("a", 300<<10) + `--><p>ordinary mailbox message</p>`
	secondSrcdoc := `<!--` + strings.Repeat("b", 300<<10) + `--><p>Next</p>`
	body := `<html><body>` +
		`<article><iframe srcdoc="` + stdhtml.EscapeString(firstSrcdoc) + `"></iframe></article>` +
		`<a href="?page=2"><iframe srcdoc="` + stdhtml.EscapeString(secondSrcdoc) + `"></iframe></a>` +
		`</body></html>`

	page, err := parseAccountHealthMailboxPage([]byte(body), "text/html", 20)
	require.NoError(t, err)
	require.Empty(t, page.nextLinks)
	require.Contains(t, strings.Join(page.blocks, "\n"), "ordinary mailbox message")
}

func TestParseAccountHealthMailboxPageSharesBudgetAcrossJSONHTMLScalars(t *testing.T) {
	firstHTML := `<iframe srcdoc="` + stdhtml.EscapeString(
		`<!--`+strings.Repeat("a", 300<<10)+`--><p>ordinary JSON mailbox message</p>`,
	) + `"></iframe>`
	secondHTML := `<iframe srcdoc="` + stdhtml.EscapeString(
		`<!--`+strings.Repeat("b", 300<<10)+`--><p>late-json-srcdoc-secret-marker</p>`,
	) + `"></iframe>`
	body, err := json.Marshal([]string{firstHTML, secondHTML})
	require.NoError(t, err)

	page, err := parseAccountHealthMailboxPage(body, "application/json", 20)
	require.NoError(t, err)
	require.Contains(t, strings.Join(page.blocks, "\n"), "ordinary JSON mailbox message")
	require.NotContains(t, strings.Join(page.blocks, "\n"), "late-json-srcdoc-secret-marker")
}

func TestParseAccountHealthMailboxPageLimitsCumulativeSrcdocParses(t *testing.T) {
	body := `<html><body><article>` +
		strings.Repeat(`<iframe srcdoc="&lt;p&gt;benign&lt;/p&gt;"></iframe>`, accountHealthMaxSrcdocParses) +
		`<iframe srcdoc="&lt;p&gt;late-srcdoc-parse-secret-marker&lt;/p&gt;"></iframe>` +
		`</article></body></html>`

	page, err := parseAccountHealthMailboxPage([]byte(body), "text/html", 20)
	require.NoError(t, err)
	require.NotContains(t, strings.Join(page.blocks, "\n"), "late-srcdoc-parse-secret-marker")
}

func TestParseAccountHealthMailboxPageExtractsJSONMessages(t *testing.T) {
	body := `[
  "Verification code 123456",
  {"subject":"OpenAI - Access Deactivated","body":"Your OpenAI account has been deactivated and can no longer be used.","date":"2026-07-21 12:00:00"}
]`
	page, err := parseAccountHealthMailboxPage([]byte(body), "application/json", 20)
	require.NoError(t, err)
	require.Len(t, page.blocks, 2)

	detection := classifyAccountHealthMessages(page.blocks, "user@example.com", 1, false, time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC))
	require.Equal(t, accountHealthStatusDeactivated, detection.Status)
	require.Equal(t, "2026-07-21 12:00:00", detection.BanDate)
}

func TestParseAccountHealthMailboxPageCountsJSONMessageObjectsOnce(t *testing.T) {
	messages := make([]map[string]any, 0, 26)
	for index := 1; index <= 26; index++ {
		body := "Ordinary mailbox message"
		if index == 26 {
			body = accountHealthTestBan
		}
		messages = append(messages, map[string]any{
			"id":      index,
			"subject": fmt.Sprintf("Message %02d", index),
			"body":    body,
		})
	}
	body, err := json.Marshal(map[string]any{"messages": messages, "total": 26})
	require.NoError(t, err)

	page, err := parseAccountHealthMailboxPage(body, "application/json", 50)
	require.NoError(t, err)
	require.Len(t, page.blocks, 26)

	detection := classifyAccountHealthMessages(page.blocks, "user@example.com", 1, false, time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC))
	require.Equal(t, accountHealthStatusDeactivated, detection.Status)
	require.Equal(t, 26, detection.MessagesScanned)
}

func TestParseAccountHealthMailboxPagePrioritizesMessageContainersOverWrapperScalars(t *testing.T) {
	wrapper := map[string]any{
		"messages": []map[string]any{{
			"subject": "OpenAI - Access Deactivated",
			"body":    accountHealthTestBan,
			"date":    "2026-07-21 12:00:00",
		}},
	}
	for index := range 50 {
		wrapper[fmt.Sprintf("a%02d", index)] = fmt.Sprintf("wrapper metadata %02d", index)
	}
	body, err := json.Marshal(wrapper)
	require.NoError(t, err)

	page, err := parseAccountHealthMailboxPage(body, "application/json", 50)
	require.NoError(t, err)
	require.Len(t, page.blocks, 1)

	detection := classifyAccountHealthMessages(page.blocks, "user@example.com", 1, false, time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC))
	require.Equal(t, accountHealthStatusDeactivated, detection.Status)
	require.Equal(t, 1, detection.MessagesScanned)
}

func TestParseAccountHealthMailboxPageIgnoresOversizedNextLink(t *testing.T) {
	body := `<html><body><article>Ordinary mailbox message</article><a rel="next" href="https://mail.example/` +
		strings.Repeat("x", accountHealthMaxURLBytes) + `">Next</a></body></html>`

	page, err := parseAccountHealthMailboxPage([]byte(body), "text/html", 20)
	require.NoError(t, err)
	require.Empty(t, page.nextLinks)
}

func TestParseAccountHealthMailboxPageSupportsContainerSubstringsAndHeadingFallback(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "container class substrings",
			body: `<html><body>
<div class="mail-item-row selected">Verification code with enough content to be an individual mailbox message.</div>
<div class="email_item_content">Your OpenAI account has been deactivated and can no longer be used. 2026-07-21.</div>
</body></html>`,
		},
		{
			name: "nested mail item list",
			body: `<html><body>
<div class="mail-item-list">
  <div class="mail-item-row">Verification code with enough content to be an individual mailbox message.</div>
  <div class="mail-item-row">Your OpenAI account has been deactivated and can no longer be used. 2026-07-21.</div>
</div>
</body></html>`,
		},
		{
			name: "heading parent fallback",
			body: `<html><body>
<section><h2>Verification code</h2><p>Your code is 123456. This message contains enough ordinary content for extraction.</p></section>
<section><h2>OpenAI - Access Deactivated</h2><p>Your OpenAI account has been deactivated and can no longer be used. 2026-07-21.</p></section>
</body></html>`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			page, err := parseAccountHealthMailboxPage([]byte(test.body), "text/html", 20)
			require.NoError(t, err)
			require.Len(t, page.blocks, 2)
			detection := classifyAccountHealthMessages(
				page.blocks,
				"user@example.com",
				1,
				page.dynamicHint,
				time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
			)
			require.Equal(t, accountHealthStatusDeactivated, detection.Status)
			require.Equal(t, 2, detection.MessagesScanned)
		})
	}
}

func TestClassifyAccountHealthMessagesCombinesPlusBanAndCounts(t *testing.T) {
	now := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	detection := classifyAccountHealthMessages(
		[]string{accountHealthTestPlus, accountHealthTestBan + "\n2026-07-21 12:00:00"},
		"user@example.com",
		2,
		false,
		now,
	)

	require.Equal(t, accountHealthStatusDeactivated, detection.Status)
	require.GreaterOrEqual(t, detection.Score, 90)
	require.True(t, detection.PlusDetected)
	require.GreaterOrEqual(t, detection.PlusScore, 90)
	require.Equal(t, "2026-07-16", detection.PlusDate)
	require.Equal(t, "2026-07-21 12:00:00", detection.BanDate)
	require.Equal(t, "UPI", detection.PaymentMethod)
	require.Equal(t, accountHealthLifespanEnded, detection.LifespanStatus)
	require.NotNil(t, detection.LifespanSeconds)
	require.Equal(t, int64((5*24+12)*60*60), *detection.LifespanSeconds)
	require.Equal(t, 2, detection.MessagesScanned)
	require.Equal(t, 2, detection.PagesScanned)
	require.NotEmpty(t, detection.Evidence)
	require.NotEmpty(t, detection.PlusEvidence)
}

func TestClassifyAccountHealthMessagesLifecycleAndFailureStates(t *testing.T) {
	now := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name                string
		blocks              []string
		expectedEmail       string
		dynamic             bool
		wantStatus          accountHealthDetectionStatus
		wantBanDate         string
		wantLifespanSeconds int64
	}{
		{
			name:          "associated account mismatch",
			blocks:        []string{accountHealthTestBan + "\n2026-07-21 12:00:00"},
			expectedEmail: "other@example.com",
			wantStatus:    accountHealthStatusMismatch,
		},
		{
			name: "reactivation associated account mismatch",
			blocks: []string{
				"OpenAI\nYour OpenAI account has been reactivated\nAssociated with different@example.com\n2026-07-21 12:00:00",
			},
			expectedEmail: "user@example.com",
			wantStatus:    accountHealthStatusMismatch,
		},
		{
			name: "warning associated account mismatch",
			blocks: []string{
				"OpenAI\nWarning about your account\nAssociated with different@example.com\nPlease review our Usage Policies.",
			},
			expectedEmail: "user@example.com",
			wantStatus:    accountHealthStatusMismatch,
		},
		{
			name:          "warning is not deactivation",
			blocks:        []string{"OpenAI\nWarning about your account\nPlease review our Usage Policies."},
			expectedEmail: "user@example.com",
			wantStatus:    accountHealthStatusWarning,
		},
		{
			name: "later recovery wins and historical ban remains",
			blocks: []string{
				accountHealthTestPlus,
				accountHealthTestBan + "\n2026-07-18 12:00:00",
				"OpenAI\nYour OpenAI account has been reactivated\n2026-07-20 09:00:00",
			},
			expectedEmail:       "user@example.com",
			wantStatus:          accountHealthStatusReactivated,
			wantBanDate:         "2026-07-18 12:00:00",
			wantLifespanSeconds: int64((2*24 + 12) * 60 * 60),
		},
		{
			name: "later deactivation wins after recovery",
			blocks: []string{
				"OpenAI\nYour OpenAI account has been reactivated\n2026-07-18 09:00:00",
				accountHealthTestBan + "\n2026-07-20 12:00:00",
			},
			expectedEmail: "user@example.com",
			wantStatus:    accountHealthStatusDeactivated,
			wantBanDate:   "2026-07-20 12:00:00",
		},
		{
			name: "same timestamp conservatively prefers deactivation",
			blocks: []string{
				"OpenAI\nYour OpenAI account has been reactivated\n2026-07-20 12:00:00",
				accountHealthTestBan + "\n2026-07-20 12:00:00",
			},
			expectedEmail: "user@example.com",
			wantStatus:    accountHealthStatusDeactivated,
			wantBanDate:   "2026-07-20 12:00:00",
		},
		{
			name: "undated deactivation is conservative against dated recovery",
			blocks: []string{
				"OpenAI\nYour OpenAI account has been reactivated\n2026-07-20 12:00:00",
				accountHealthTestBan,
			},
			expectedEmail: "user@example.com",
			wantStatus:    accountHealthStatusDeactivated,
		},
		{
			name: "warning does not override historical deactivation",
			blocks: []string{
				accountHealthTestBan + "\n2026-07-18 12:00:00",
				"OpenAI\nWarning about your account\n2026-07-20 12:00:00",
			},
			expectedEmail: "user@example.com",
			wantStatus:    accountHealthStatusDeactivated,
			wantBanDate:   "2026-07-18 12:00:00",
		},
		{
			name:          "benign mailbox has no evidence",
			blocks:        []string{"OpenAI verification code\nYour code is 123456 and expires shortly."},
			expectedEmail: "user@example.com",
			wantStatus:    accountHealthStatusNotFound,
		},
		{
			name:          "dynamic empty mailbox is parse failure",
			blocks:        []string{"Loading"},
			expectedEmail: "user@example.com",
			dynamic:       true,
			wantStatus:    accountHealthStatusParseError,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detection := classifyAccountHealthMessages(test.blocks, test.expectedEmail, 1, test.dynamic, now)
			require.Equal(t, test.wantStatus, detection.Status)
			require.Equal(t, test.wantBanDate, detection.BanDate)
			if test.wantLifespanSeconds > 0 {
				require.Equal(t, accountHealthLifespanEnded, detection.LifespanStatus)
				require.NotNil(t, detection.LifespanSeconds)
				require.Equal(t, test.wantLifespanSeconds, *detection.LifespanSeconds)
			}
		})
	}
}

func TestClassifyAccountHealthLifecycleRequiresOpenAIAnchor(t *testing.T) {
	tests := []struct {
		name  string
		block string
	}{
		{name: "generic deactivation", block: "Your account has been deactivated. This account can no longer be used."},
		{name: "generic recovery", block: "Your account has been reactivated and access is restored."},
		{name: "generic warning", block: "Warning about your account. Please review our usage policies."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detection := classifyAccountHealthMessages(
				[]string{test.block},
				"user@example.com",
				1,
				false,
				time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
			)
			require.Equal(t, accountHealthStatusNotFound, detection.Status)
		})
	}
}

func TestClassifyAccountHealthMessageAcceptsUnicodeDashSubject(t *testing.T) {
	for _, test := range []struct {
		name string
		dash string
	}{
		{name: "en dash", dash: "\u2013"},
		{name: "em dash", dash: "\u2014"},
	} {
		t.Run(test.name, func(t *testing.T) {
			detection := classifyAccountHealthMessages(
				[]string{"OpenAI " + test.dash + " Access Deactivated\n2026-07-21"},
				"user@example.com",
				1,
				false,
				time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
			)

			require.Equal(t, accountHealthStatusDeactivated, detection.Status)
		})
	}
}

func TestClassifyAccountHealthMessageDoesNotTreatQuotedBodyLineAsSubject(t *testing.T) {
	detection := classifyAccountHealthMessage(`Monthly security digest
The following historical title was quoted by the sender:
Subject: OpenAI - Access Deactivated
No account status change was reported in this message.`)

	require.Equal(t, accountHealthStatusNotFound, detection.Status)
	require.Equal(t, "Monthly security digest", detection.Subject)
}

func TestClassifyAccountHealthMessageAcceptsLeadingSubjectHeader(t *testing.T) {
	detection := classifyAccountHealthMessages(
		[]string{"Subject: OpenAI - Access Deactivated\n2026-07-21"},
		"user@example.com",
		1,
		false,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	)

	require.Equal(t, accountHealthStatusDeactivated, detection.Status)
	require.Equal(t, "OpenAI - Access Deactivated", detection.Subject)
}

func TestClassifyAccountHealthPlusAvoidsCancellationMarketingAndUnrelatedPayment(t *testing.T) {
	now := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		blocks []string
	}{
		{
			name: "cancellation receipt",
			blocks: []string{`Your ChatGPT Plus subscription has been canceled.
Order number: sub_CanceledSynthetic
Order date: Jul 19, 2026
Payment method: UPI`},
		},
		{name: "marketing", blocks: []string{"Try ChatGPT Plus today and explore more features."}},
		{
			name: "failed payment with receipt metadata",
			blocks: []string{`ChatGPT Plus payment failed.
Order number: sub_FailedSynthetic
Order date: Jul 20, 2026
Payment method: Card`},
		},
		{
			name: "receipt metadata without success semantics",
			blocks: []string{`ChatGPT Plus
Order number: sub_UnconfirmedSynthetic
Order date: Jul 20, 2026
Payment method: Card`},
		},
		{name: "support article", blocks: []string{"How to check whether your ChatGPT Plus subscription is active"}},
		{name: "question", blocks: []string{"Is your ChatGPT Plus subscription active?"}},
		{name: "statement-shaped question", blocks: []string{"Your ChatGPT Plus subscription is active?"}},
		{name: "conditional guidance", blocks: []string{"If your ChatGPT Plus subscription is active, manage it in settings."}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detection := classifyAccountHealthMessages(test.blocks, "user@example.com", 1, false, now)
			require.False(t, detection.PlusDetected)
		})
	}

	confirmedStatus := classifyAccountHealthMessages(
		[]string{"Your ChatGPT Plus subscription is now active."},
		"user@example.com",
		1,
		false,
		now,
	)
	require.True(t, confirmedStatus.PlusDetected)

	plusWithoutPayment := `You've successfully subscribed to ChatGPT Plus.
Order number: sub_NoPaymentSynthetic
Order date: Jul 19, 2026`
	detection := classifyAccountHealthMessages(
		[]string{plusWithoutPayment, "Online store receipt\nPayment method: Gift card"},
		"user@example.com",
		1,
		false,
		now,
	)
	require.True(t, detection.PlusDetected)
	require.Empty(t, detection.PaymentMethod)

	currentAccountPlus := plusWithoutPayment + "\nAssociated with user@example.com."
	otherAccountPayment := `You've successfully subscribed to ChatGPT Plus.
Associated with other@example.com.
Order number: sub_OtherSynthetic
Order date: Jul 20, 2026
Payment method: Card`
	detection = classifyAccountHealthMessages(
		[]string{currentAccountPlus, otherAccountPayment},
		"user@example.com",
		1,
		false,
		now,
	)
	require.True(t, detection.PlusDetected)
	require.Empty(t, detection.PaymentMethod)

	unscopedFailedPayment := `ChatGPT Plus payment failed.
Order number: sub_FailedFallbackSynthetic
Order date: Jul 20, 2026
Payment method: Card`
	detection = classifyAccountHealthMessages(
		[]string{plusWithoutPayment, unscopedFailedPayment},
		"user@example.com",
		1,
		false,
		now,
	)
	require.True(t, detection.PlusDetected)
	require.Empty(t, detection.PaymentMethod)

	unscopedPlusPayment := `You've successfully subscribed to ChatGPT Plus.
Order number: sub_UnscopedSynthetic
Order date: Jul 20, 2026
Payment method: Card`
	detection = classifyAccountHealthMessages(
		[]string{currentAccountPlus, unscopedPlusPayment},
		"user@example.com",
		1,
		false,
		now,
	)
	require.True(t, detection.PlusDetected)
	require.Empty(t, detection.PaymentMethod)
}

func TestClassifyAccountHealthPlusTrimsOrderMetadataBeforePaymentLimit(t *testing.T) {
	detection := classifyAccountHealthMessages(
		[]string{`You've successfully subscribed to ChatGPT Plus.
Payment method: Visa ending in 4242 Order number: sub_SyntheticOrderIdentifierLongEnoughToExceedTheRawPaymentLimit
Order date: Jul 19, 2026`},
		"user@example.com",
		1,
		false,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	)

	require.True(t, detection.PlusDetected)
	require.Equal(t, "Visa ending in 4242", detection.PaymentMethod)
}

func TestClassifyAccountHealthPlusDropsUnsafePaymentMethods(t *testing.T) {
	tests := []struct {
		name    string
		payment string
	}{
		{name: "payment token", payment: "sk_live_canary_secret"},
		{name: "full card number", payment: "4111111111111111"},
		{name: "bank account", payment: "DE89370400440532013000"},
		{name: "prose", payment: "in settings."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detection := classifyAccountHealthMessages(
				[]string{"You've successfully subscribed to ChatGPT Plus.\nPayment method: " + test.payment},
				"user@example.com",
				1,
				false,
				time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
			)

			require.True(t, detection.PlusDetected)
			require.Empty(t, detection.PaymentMethod)
		})
	}
}

func TestClassifyAccountHealthPlusDoesNotTreatPaymentProseAsALabel(t *testing.T) {
	detection := classifyAccountHealthMessages(
		[]string{"You've successfully subscribed to ChatGPT Plus.\nYou can update your payment method in settings."},
		"user@example.com",
		1,
		false,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	)

	require.True(t, detection.PlusDetected)
	require.Empty(t, detection.PaymentMethod)
}

func TestClassifyAccountHealthPlusRejectsAmbiguousLocalizedSubscriptionText(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{name: "French marketing", text: "Découvrez notre abonnement ChatGPT Plus dès aujourd'hui."},
		{name: "Spanish marketing", text: "Conoce nuestra suscripción ChatGPT Plus y sus ventajas."},
		{name: "Portuguese marketing", text: "Conheça a assinatura ChatGPT Plus e todos os recursos."},
		{name: "Italian marketing", text: "Scopri il nostro abbonamento ChatGPT Plus."},
		{name: "German marketing", text: "Mehr über das ChatGPT Plus Abonnement erfahren."},
		{name: "French cancellation reversed", text: "Votre abonnement ChatGPT Plus a été annulé."},
		{name: "Spanish cancellation reversed", text: "Tu suscripción ChatGPT Plus fue cancelada."},
		{name: "Portuguese cancellation reversed", text: "Sua assinatura ChatGPT Plus foi cancelada."},
		{name: "Italian cancellation reversed", text: "Il tuo abbonamento ChatGPT Plus è stato annullato."},
		{name: "French not active", text: "Votre abonnement ChatGPT Plus n'est pas activé."},
		{name: "Spanish not active", text: "Tu suscripción ChatGPT Plus no está activa."},
		{name: "Portuguese not active", text: "Sua assinatura ChatGPT Plus não está ativa."},
		{name: "Italian not active", text: "Il tuo abbonamento ChatGPT Plus non è attivo."},
		{name: "English not active", text: "Your ChatGPT Plus subscription is not active."},
		{name: "Japanese not active", text: "ChatGPT Plus サブスクリプションは有効ではありません。"},
		{name: "Chinese not active", text: "ChatGPT Plus 订阅尚未生效。"},
		{name: "German not active", text: "Ihr ChatGPT Plus Abonnement ist nicht aktiv."},
		{name: "Korean not active", text: "ChatGPT Plus 구독이 아직 활성화되지 않았습니다."},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detection := classifyAccountHealthMessages(
				[]string{test.text},
				"user@example.com",
				1,
				false,
				time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
			)
			require.False(t, detection.PlusDetected)
		})
	}
}

func TestClassifyAccountHealthMessagesPrioritizesExpectedAccountLifecycle(t *testing.T) {
	blocks := []string{
		`OpenAI
Your OpenAI account has been reactivated
Associated with user@example.com
2026-07-20 09:00:00`,
		`OpenAI - Access Deactivated
Your OpenAI account associated with other@example.com has been deactivated and can no longer be used.
2026-07-21 09:00:00`,
	}

	detection := classifyAccountHealthMessages(
		blocks,
		"user@example.com",
		1,
		false,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	)

	require.Equal(t, accountHealthStatusReactivated, detection.Status)
	require.Equal(t, "user@example.com", detection.AssociatedEmail)
}

func TestClassifyAccountHealthMessagesPrioritizesExpectedAccountOverUnscopedLifecycle(t *testing.T) {
	blocks := []string{
		`OpenAI
Your OpenAI account has been reactivated
Associated with user@example.com
2026-07-20 09:00:00`,
		`OpenAI - Access Deactivated
Your OpenAI account has been deactivated and can no longer be used.
2026-07-21 09:00:00`,
	}

	detection := classifyAccountHealthMessages(
		blocks,
		"user@example.com",
		1,
		false,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	)

	require.Equal(t, accountHealthStatusReactivated, detection.Status)
	require.Equal(t, "user@example.com", detection.AssociatedEmail)
	require.Empty(t, detection.BanDate)
}

func TestClassifyAccountHealthMessagesIgnoresPlusEvidenceForAnotherAccount(t *testing.T) {
	now := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	otherAccountPlus := strings.Replace(
		accountHealthTestPlus,
		"You've successfully subscribed to ChatGPT Plus.",
		"You've successfully subscribed to ChatGPT Plus. Associated with other@example.com.",
		1,
	)
	otherAccountPlus = strings.Replace(otherAccountPlus, "Jul 16, 2026", "Jul 25, 2026", 1)
	detection := classifyAccountHealthMessages(
		[]string{accountHealthTestBan + "\n2026-07-21 12:00:00", otherAccountPlus},
		"user@example.com",
		2,
		false,
		now,
	)

	require.Equal(t, accountHealthStatusDeactivated, detection.Status)
	require.False(t, detection.PlusDetected)
}

func TestClassifyAccountHealthPlusAcrossSupportedLanguages(t *testing.T) {
	tests := []struct {
		language string
		success  string
		payment  string
	}{
		{language: "English", success: "You've successfully subscribed to ChatGPT Plus.", payment: "Payment method: Card"},
		{language: "日本語", success: "ChatGPT Plus に正常に登録されました。", payment: "決済方法：UPI"},
		{language: "中文", success: "您已成功订阅 ChatGPT Plus。", payment: "付款方式：支付宝"},
		{language: "Deutsch", success: "Sie haben ChatGPT Plus erfolgreich abonniert.", payment: "Zahlungsmethode: Karte"},
		{language: "Français", success: "Vous êtes désormais abonné à ChatGPT Plus.", payment: "Mode de paiement : Carte"},
		{language: "Español", success: "Te has suscrito correctamente a ChatGPT Plus.", payment: "Método de pago: Tarjeta"},
		{language: "Português", success: "Sua assinatura do ChatGPT Plus foi confirmada.", payment: "Método de pagamento: Pix"},
		{language: "Italiano", success: "Il tuo abbonamento a ChatGPT Plus è stato confermato.", payment: "Metodo di pagamento: Carta"},
		{language: "한국어", success: "ChatGPT Plus 구독이 완료되었습니다.", payment: "결제 수단: 카드"},
	}

	for _, test := range tests {
		t.Run(test.language, func(t *testing.T) {
			message := strings.Join([]string{
				test.success,
				"Order number: sub_MultilingualSynthetic",
				"Order date: Jul 19, 2026",
				test.payment,
			}, "\n")
			detection := classifyAccountHealthMessages(
				[]string{message},
				"user@example.com",
				1,
				false,
				time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
			)
			require.True(t, detection.PlusDetected)
			require.Equal(t, test.language, detection.PlusLanguage)
			require.NotEmpty(t, detection.PaymentMethod)
		})
	}
}

func TestClassifyAccountHealthScopesLocalizedAssociatedEmails(t *testing.T) {
	tests := []struct {
		language    string
		association string
		banText     string
		plusText    string
	}{
		{
			language:    "日本語",
			association: "関連付け: %s",
			banText:     "OpenAI アカウントが無効化されました。このアカウントは今後利用できません。",
			plusText:    "ChatGPT Plus に正常に登録されました。",
		},
		{
			language:    "中文",
			association: "关联邮箱: %s",
			banText:     "您的 OpenAI 账户已被停用，无法再继续使用。",
			plusText:    "您已成功订阅 ChatGPT Plus。",
		},
		{
			language:    "Deutsch",
			association: "Verknüpft mit %s",
			banText:     "Ihr OpenAI-Konto wurde deaktiviert und kann nicht mehr verwendet werden.",
			plusText:    "Sie haben ChatGPT Plus erfolgreich abonniert.",
		},
		{
			language:    "Français",
			association: "Associé à %s",
			banText:     "Votre compte OpenAI a été désactivé et ne peut plus être utilisé.",
			plusText:    "Vous êtes désormais abonné à ChatGPT Plus.",
		},
		{
			language:    "Español",
			association: "Asociado a %s",
			banText:     "Tu cuenta de OpenAI ha sido desactivada y ya no se puede utilizar.",
			plusText:    "Te has suscrito correctamente a ChatGPT Plus.",
		},
		{
			language:    "Português",
			association: "Associada a %s",
			banText:     "Sua conta OpenAI foi desativada e não pode mais ser usada.",
			plusText:    "Sua assinatura do ChatGPT Plus foi confirmada.",
		},
		{
			language:    "Italiano",
			association: "Associato a %s",
			banText:     "Il tuo account OpenAI è stato disattivato e non può più essere utilizzato.",
			plusText:    "Il tuo abbonamento a ChatGPT Plus è stato confermato.",
		},
		{
			language:    "한국어",
			association: "연결된 이메일: %s",
			banText:     "OpenAI 계정이 비활성화되었습니다. 더 이상 사용할 수 없습니다.",
			plusText:    "ChatGPT Plus 구독이 완료되었습니다.",
		},
	}

	for _, test := range tests {
		t.Run(test.language, func(t *testing.T) {
			for _, account := range []struct {
				name  string
				email string
				match bool
			}{
				{name: "matching", email: "user@example.com", match: true},
				{name: "different", email: "other@example.com", match: false},
			} {
				t.Run(account.name+" lifecycle", func(t *testing.T) {
					message := fmt.Sprintf(test.association, account.email) + "\n" + test.banText + "\n2026-07-21 09:30:00"
					detection := classifyAccountHealthMessages(
						[]string{message},
						"user@example.com",
						1,
						false,
						time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
					)
					if account.match {
						require.Equal(t, accountHealthStatusDeactivated, detection.Status)
					} else {
						require.Equal(t, accountHealthStatusMismatch, detection.Status)
					}
					require.Equal(t, account.email, detection.AssociatedEmail)
				})

				t.Run(account.name+" plus", func(t *testing.T) {
					message := fmt.Sprintf(test.association, account.email) + "\n" + test.plusText
					detection := classifyAccountHealthMessages(
						[]string{message},
						"user@example.com",
						1,
						false,
						time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
					)
					require.Equal(t, account.match, detection.PlusDetected)
				})
			}
		})
	}
}

func TestClassifyAccountHealthNormalizesFullWidthASCII(t *testing.T) {
	detection := classifyAccountHealthMessages(
		[]string{"ＣｈａｔＧＰＴ　Ｐｌｕｓ subscription is active.\nOrder number: sub_FullWidthSynthetic"},
		"user@example.com",
		1,
		false,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	)

	require.True(t, detection.PlusDetected)
	require.Equal(t, "English", detection.PlusLanguage)
}

func TestClassifyAccountHealthDeactivationAcrossSupportedLanguages(t *testing.T) {
	tests := []struct {
		language string
		message  string
	}{
		{language: "English", message: "Your OpenAI account has been deactivated. This account can no longer be used."},
		{language: "日本語", message: "OpenAI アカウントが無効化されました。このアカウントは今後利用できません。"},
		{language: "中文", message: "您的 OpenAI 账户已被停用，无法再继续使用。"},
		{language: "Deutsch", message: "Ihr OpenAI-Konto wurde deaktiviert und kann nicht mehr verwendet werden."},
		{language: "Français", message: "Votre compte OpenAI a été désactivé et ne peut plus être utilisé."},
		{language: "Español", message: "Tu cuenta de OpenAI ha sido desactivada y ya no se puede utilizar."},
		{language: "Português", message: "Sua conta OpenAI foi desativada e não pode mais ser usada."},
		{language: "Italiano", message: "Il tuo account OpenAI è stato disattivato e non può più essere utilizzato."},
		{language: "한국어", message: "OpenAI 계정이 비활성화되었습니다. 더 이상 사용할 수 없습니다."},
	}

	for _, test := range tests {
		t.Run(test.language, func(t *testing.T) {
			detection := classifyAccountHealthMessages(
				[]string{test.message + "\n2026-07-21 09:30:00"},
				"user@example.com",
				1,
				false,
				time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
			)
			require.Equal(t, accountHealthStatusDeactivated, detection.Status)
			require.Equal(t, test.language, detection.Language)
		})
	}
}

func TestClassifyAccountHealthPlusAfterHistoricalBanIsRecovered(t *testing.T) {
	detection := classifyAccountHealthMessages(
		[]string{
			accountHealthTestBan + "\n2026-07-01 10:00:00",
			strings.Replace(accountHealthTestPlus, "Jul 16, 2026", "Jul 16, 2026 12:00", 1),
		},
		"user@example.com",
		1,
		false,
		time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
	)

	require.Equal(t, accountHealthStatusReactivated, detection.Status)
	require.Equal(t, "2026-07-01 10:00:00", detection.BanDate)
	require.Equal(t, accountHealthLifespanActive, detection.LifespanStatus)
	require.NotNil(t, detection.LifespanSeconds)
}

func TestClassifyAccountHealthMessagesUsesTheFirstValidDateInMessageOrder(t *testing.T) {
	ban := accountHealthTestBan + `
Sent: Jul 20, 2026 09:00:00
Appeal deadline: 2026-08-01`
	plus := strings.Replace(accountHealthTestPlus, "Jul 16, 2026", "Jul 25, 2026 12:00:00", 1)

	detection := classifyAccountHealthMessages(
		[]string{ban, plus},
		"user@example.com",
		1,
		false,
		time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
	)

	require.Equal(t, accountHealthStatusReactivated, detection.Status)
	require.Equal(t, "2026-07-20 09:00:00", detection.MessageDate)
	require.Equal(t, "2026-07-20 09:00:00", detection.BanDate)
}

func TestAccountHealthParseDateSkipsInvalidEarlierCandidates(t *testing.T) {
	parsed, hasTime := accountHealthParseDate("Sent: Smarch 2, 2026. Received: Jul 20, 2026 09:00:00")

	require.True(t, hasTime)
	require.Equal(t, time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC), parsed)
}

func TestClassifyAccountHealthMessagesDoesNotOrderMixedPrecisionDatesWithinOneDay(t *testing.T) {
	t.Run("plus does not imply recovery", func(t *testing.T) {
		plus := strings.Replace(accountHealthTestPlus, "Jul 16, 2026", "Jul 21, 2026 12:00:00", 1)
		detection := classifyAccountHealthMessages(
			[]string{accountHealthTestBan + "\n2026-07-21", plus},
			"user@example.com",
			1,
			false,
			time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
		)

		require.Equal(t, accountHealthStatusDeactivated, detection.Status)
		require.Equal(t, accountHealthLifespanInvalidDate, detection.LifespanStatus)
		require.Nil(t, detection.LifespanSeconds)
	})

	t.Run("recovery notice does not override deactivation", func(t *testing.T) {
		detection := classifyAccountHealthMessages(
			[]string{
				accountHealthTestBan + "\n2026-07-21",
				"OpenAI\nYour OpenAI account has been reactivated\n2026-07-21 12:00:00",
			},
			"user@example.com",
			1,
			false,
			time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
		)

		require.Equal(t, accountHealthStatusDeactivated, detection.Status)
	})

	t.Run("different days remain orderable", func(t *testing.T) {
		plus := strings.Replace(accountHealthTestPlus, "Jul 16, 2026", "Jul 22, 2026 12:00:00", 1)
		detection := classifyAccountHealthMessages(
			[]string{accountHealthTestBan + "\n2026-07-21", plus},
			"user@example.com",
			1,
			false,
			time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
		)

		require.Equal(t, accountHealthStatusReactivated, detection.Status)
		require.Equal(t, accountHealthLifespanActive, detection.LifespanStatus)
	})
}

func TestClassifyAccountHealthLatestPlusAfterBanRestoresLifecycle(t *testing.T) {
	plusBefore := strings.Replace(accountHealthTestPlus, "Jul 16, 2026", "Jul 1, 2026", 1)
	plusAfter := strings.Replace(accountHealthTestPlus, "Jul 16, 2026", "Jul 10, 2026", 1)
	detection := classifyAccountHealthMessages(
		[]string{
			plusAfter,
			accountHealthTestBan + "\n2026-07-05 12:00:00",
			plusBefore,
		},
		"user@example.com",
		1,
		false,
		time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
	)

	require.Equal(t, accountHealthStatusReactivated, detection.Status)
	require.Equal(t, "2026-07-01", detection.PlusDate)
	require.Equal(t, "2026-07-05 12:00:00", detection.BanDate)
	require.Equal(t, accountHealthLifespanEnded, detection.LifespanStatus)
	require.NotNil(t, detection.LifespanSeconds)
	require.Equal(t, int64((4*24+12)*60*60), *detection.LifespanSeconds)
}

func TestClassifyAccountHealthMessagesUsesTimezoneOffsetsForLifecycleOrdering(t *testing.T) {
	plus := `You've successfully subscribed to ChatGPT Plus.
Order date: Jul 21, 2026 00:30:00 +14:00
Payment method: Visa ending in 4242`
	ban := accountHealthTestBan + "\n2026-07-20 23:00:00 -1200"

	detection := classifyAccountHealthMessages(
		[]string{plus, ban},
		"user@example.com",
		1,
		false,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	)

	require.Equal(t, accountHealthStatusDeactivated, detection.Status)
	require.Equal(t, "2026-07-20 10:30:00", detection.PlusDate)
	require.Equal(t, "2026-07-21 11:00:00", detection.BanDate)
	require.Equal(t, accountHealthLifespanEnded, detection.LifespanStatus)
	require.NotNil(t, detection.LifespanSeconds)
	require.Equal(t, int64(24*60*60+30*60), *detection.LifespanSeconds)
}

func TestClassifyAccountHealthMessagesConsumesFractionalAndPrefixedTimezoneOffsets(t *testing.T) {
	plus := `You've successfully subscribed to ChatGPT Plus.
Order date: 2026-07-21T00:30:00.000UTC+14:00
Payment method: Visa ending in 4242`
	ban := accountHealthTestBan + "\n2026-07-20T23:00:00.000GMT-1200"

	detection := classifyAccountHealthMessages(
		[]string{plus, ban},
		"user@example.com",
		1,
		false,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	)

	require.Equal(t, accountHealthStatusDeactivated, detection.Status)
	require.Equal(t, "2026-07-20 10:30:00", detection.PlusDate)
	require.Equal(t, "2026-07-21 11:00:00", detection.BanDate)
	require.Equal(t, accountHealthLifespanEnded, detection.LifespanStatus)
	require.NotNil(t, detection.LifespanSeconds)
	require.Equal(t, int64(24*60*60+30*60), *detection.LifespanSeconds)
}

func TestAccountHealthParseDatePreservesFractionalSeconds(t *testing.T) {
	parsed, hasTime := accountHealthParseDate("2026-07-21T00:30:00.125+14:00")

	require.True(t, hasTime)
	require.Equal(t, time.Date(2026, 7, 20, 10, 30, 0, 125_000_000, time.UTC), parsed)
}

func TestParseAccountHealthMailboxPageRejectsExcessiveJSONTokensBeforeObjectTreeExpansion(t *testing.T) {
	body := []byte("[" + strings.Repeat("0,", accountHealthMaxJSONTokens) + "0]")

	page, err := parseAccountHealthMailboxPage(body, "application/json", 50)

	require.Empty(t, page.blocks)
	require.ErrorIs(t, err, errAccountHealthMailboxJSONTooComplex)
}

func TestParseAccountHealthMailboxPageRejectsExcessiveEmptyJSONContainers(t *testing.T) {
	tests := []struct {
		name      string
		container string
	}{
		{name: "objects", container: "{}"},
		{name: "arrays", container: "[]"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			items := strings.Repeat(test.container+",", accountHealthMaxJSONContainers-1) + test.container
			body := []byte("[" + items + "]")

			page, err := parseAccountHealthMailboxPage(body, "application/json", 50)

			require.Empty(t, page.blocks)
			require.ErrorIs(t, err, errAccountHealthMailboxJSONTooComplex)
		})
	}
}

func TestParseAccountHealthMailboxPageRejectsExcessiveJSONDepth(t *testing.T) {
	body := []byte(
		strings.Repeat("[", accountHealthMaxJSONDepth+1) +
			"0" +
			strings.Repeat("]", accountHealthMaxJSONDepth+1),
	)

	page, err := parseAccountHealthMailboxPage(body, "application/json", 50)

	require.Empty(t, page.blocks)
	require.ErrorIs(t, err, errAccountHealthMailboxJSONTooComplex)
}

func TestAccountHealthLabeledValueHonorsLineBudget(t *testing.T) {
	withinBudget := strings.Repeat("ignored\n", accountHealthMaxLabeledLines-1) + "Payment method: Pix"
	require.Equal(t, "Pix", accountHealthLabeledValue(withinBudget, accountHealthPaymentLabels))

	beyondBudget := strings.Repeat("ignored\n", accountHealthMaxLabeledLines) + "Payment method: secret"
	require.Empty(t, accountHealthLabeledValue(beyondBudget, accountHealthPaymentLabels))
}

func TestParseAccountHealthMailboxPageRejectsMalformedJSON(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType string
	}{
		{name: "truncated object", body: `{"messages":["OpenAI account has been deactivated"`, contentType: "application/json"},
		{name: "truncated array", body: `["OpenAI account has been deactivated"`, contentType: "application/json"},
		{name: "invalid token", body: `{"messages":[invalid]}`, contentType: "application/problem+json"},
		{name: "trailing garbage", body: `{"messages":[]} trailing`, contentType: "application/json"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			page, err := parseAccountHealthMailboxPage([]byte(test.body), test.contentType, 20)
			require.Error(t, err)
			require.Empty(t, page.blocks)
		})
	}
}

func TestParseAccountHealthMailboxPageFallsBackFromInvalidSniffedJSONToText(t *testing.T) {
	body := `[OpenAI]
Your OpenAI account has been deactivated and can no longer be used.
2026-07-21 09:30:00`

	page, err := parseAccountHealthMailboxPage([]byte(body), "text/plain", 20)

	require.NoError(t, err)
	require.Len(t, page.blocks, 1)
	detection := classifyAccountHealthMessages(
		page.blocks,
		"user@example.com",
		1,
		false,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	)
	require.Equal(t, accountHealthStatusDeactivated, detection.Status)
}

func TestParseAccountHealthMailboxPageUsesParsedMediaTypeForJSONDetection(t *testing.T) {
	body := `[OpenAI]
Your OpenAI account has been deactivated and can no longer be used.
2026-07-21 09:30:00`

	page, err := parseAccountHealthMailboxPage([]byte(body), `text/plain; name="mail.json"`, 20)

	require.NoError(t, err)
	detection := classifyAccountHealthMessages(
		page.blocks,
		"user@example.com",
		1,
		false,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	)
	require.Equal(t, accountHealthStatusDeactivated, detection.Status)
}

func TestParseAccountHealthMailboxPageFallsBackAfterSniffedJSONComplexityLimit(t *testing.T) {
	body := strings.Repeat("[", accountHealthMaxJSONDepth+1) + `OpenAI]
Your OpenAI account has been deactivated and can no longer be used.
2026-07-21 09:30:00`

	page, err := parseAccountHealthMailboxPage([]byte(body), "text/plain", 20)

	require.NoError(t, err)
	detection := classifyAccountHealthMessages(
		page.blocks,
		"user@example.com",
		1,
		false,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	)
	require.Equal(t, accountHealthStatusDeactivated, detection.Status)
}
