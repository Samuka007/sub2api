export default {
  accountHealthDetector: {
    title: 'Account Health Check',
    description: 'Select OpenAI groups, load their existing accounts, and use account notes to check subscription and deactivation evidence',
    groups: {
      title: 'Select groups to check',
      selectedCount: '{count} groups selected',
      searchPlaceholder: 'Search group name or ID',
      searchLabel: 'Search checkable groups',
      empty: 'No matching OpenAI groups'
    },
    controls: {
      concurrency: 'Concurrency'
    },
    actions: {
      loadAccounts: 'Load accounts',
      scanAccounts: 'Check all accounts ({count})',
      stop: 'Stop',
      reloadGroups: 'Refresh groups',
      selectVisibleGroups: 'Select visible groups',
      clearGroups: 'Clear groups',
      selectFilteredAccounts: 'Select filtered accounts',
      clearAccountSelection: 'Clear account selection',
      exportSelectedNotes: 'Export selected notes ({count})',
      deleteSelected: 'Delete selected ({count})',
      deleteAll: 'Delete all ({count})',
      deleteAccount: 'Delete account {name}'
    },
    progress: {
      label: 'Progress'
    },
    summary: {
      selectedGroups: 'Groups',
      loadedAccounts: 'Accounts',
      selectedAccounts: 'Selected accounts',
      checked: 'Checked',
      plus: 'Plus valid',
      banned: 'Deactivated',
      warning: 'Warnings',
      formatErrors: 'Format errors',
      failed: 'Errors or stopped'
    },
    filters: {
      searchPlaceholder: 'Search account, group, email, evidence, or ID',
      searchLabel: 'Search account check results',
      localStatusLabel: 'Filter by account status',
      outcomeLabel: 'Filter by check outcome',
      banStatusLabel: 'Filter by deactivation status',
      plusStatusLabel: 'Filter by Plus status',
      sortFieldLabel: 'Sort field',
      sortAscending: 'Sorted ascending; switch to descending',
      sortDescending: 'Sorted descending; switch to ascending',
      allLocalStatuses: 'All account statuses',
      allOutcomes: 'All check outcomes',
      allBanStatuses: 'All deactivation statuses',
      allPlusStatuses: 'All Plus statuses',
      visibleCount: '{count} visible accounts'
    },
    columns: {
      account: 'Account',
      groups: 'Groups',
      accountStatus: 'Account status',
      outcome: 'Check outcome',
      banStatus: 'Deactivation',
      plusStatus: 'Plus status',
      plusDate: 'Plus email date',
      paymentMethod: 'Payment method',
      banDate: 'Deactivation email date',
      lifespan: 'Survival duration',
      score: 'Evidence score',
      messagePageCount: 'Messages / pages',
      evidence: 'Matched evidence',
      elapsed: 'Elapsed',
      checkedAt: 'Checked at'
    },
    localStatus: {
      active: 'Active',
      inactive: 'Inactive',
      error: 'Error'
    },
    outcome: {
      idle: 'Not checked',
      queued: 'Queued',
      checking: 'Checking',
      deactivated: 'Deactivated',
      warning: 'Warning',
      reactivated: 'Reactivated',
      no_evidence: 'No deactivation evidence',
      mismatch: 'Account mismatch',
      fetch_error: 'Fetch failed',
      parse_error: 'Parse failed',
      format_error: 'Invalid format; check failed',
      stopped: 'Stopped',
      failed: 'Check failed'
    },
    banStatus: {
      unknown: 'Pending',
      not_banned: 'No deactivation found',
      banned: 'Deactivated',
      reactivated: 'Reactivated',
      warning: 'Warning',
      mismatch: 'Account mismatch'
    },
    plusStatus: {
      unknown: 'Pending',
      not_detected: 'Not detected',
      detected: 'Plus valid',
      detectedWithScore: 'Plus valid · {score} pts',
      banned: 'Deactivated'
    },
    lifespan: {
      active: 'Active · {days}d {hours}h',
      ended: '{days}d {hours}h',
      banDateUnknown: 'Deactivation date unknown',
      invalidDate: 'Invalid date range'
    },
    evidence: {
      account_mismatch: 'Email account does not match the checked account',
      plus_after_deactivation: 'Plus email is newer than the historical deactivation email',
      dynamic_mailbox: 'No recognizable email found; the mailbox may require JavaScript',
      reactivated_notice: 'Matched account reactivation notice',
      deactivation_semantics: 'Matched account deactivation wording',
      openai_anchor: 'Matched OpenAI identity',
      account_unavailable: 'Matched account unavailable wording',
      policy_violation: 'Matched terms or policy violation wording',
      appeal_available: 'Matched appeal instructions',
      deactivation_subject: 'Matched deactivation email subject',
      warning_notice: 'Matched account warning notice',
      plus_marker: 'Matched ChatGPT Plus identity',
      subscription_confirmed: 'Matched confirmed subscription wording',
      order_number: 'Matched order number',
      payment_method: 'Matched payment method',
      order_date: 'Matched order date',
      subscription_management: 'Matched subscription management or renewal wording'
    },
    formatIssue: {
      empty: 'The account notes are empty',
      missing_email: 'No email address was found in the account notes',
      missing_url: 'No HTTP(S) check URL was found in the account notes',
      multiple_records: 'The account notes contain multiple account records',
      invalid_url: 'The check URL in the account notes is invalid',
      mixed_invalid_lines: 'The account notes mix a valid record with invalid lines'
    },
    values: {
      score: '{value} pts',
      messageDate: 'Evidence email date: {value}',
      associatedEmail: 'Associated email: {value}'
    },
    reasons: {
      requestFailed: 'The check request could not be completed'
    },
    selectionLabel: 'Select account {name}',
    empty: {
      selectGroups: 'Select the groups to check first',
      loadAccounts: 'Groups selected; load their accounts',
      noAccounts: 'No OpenAI accounts are bound to the selected groups'
    },
    deleteDialog: {
      title: 'Delete accounts',
      single: 'Delete account "{name}"? This action cannot be undone.',
      batch: 'Delete these {count} accounts? This action cannot be undone.'
    },
    messages: {
      loadGroupsFailed: 'Failed to load OpenAI groups',
      loadAccountsFailed: 'Failed to load accounts in the selected groups',
      noGroupSelection: 'Select at least one group',
      noAccounts: 'There are no accounts to check',
      groupLimit: 'Select at most {count} groups at a time',
      accountNoteExportLimit: 'Select no more than {count} accounts for each note export',
      accountNotesExported: 'Exported notes for {count} accounts',
      exportAccountNotesFailed: 'Failed to export notes for the selected accounts',
      deleteSuccess: 'Deleted {count} accounts',
      deletePartial: 'Deletion finished: {success} succeeded and {failed} failed',
      deleteFailed: 'Failed to delete accounts'
    }
  }
}
