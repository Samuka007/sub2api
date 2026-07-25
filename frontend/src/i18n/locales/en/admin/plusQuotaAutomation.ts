export default {
  plusQuotaAutomation: {
    title: 'Plus Usage Reset',
    description: 'Schedule Plus group usage checks and manage accounts returning 401',
    config: {
      enabled: 'Automatic scans',
      group: 'Target group',
      intervalMinutes: 'Scan interval (minutes)',
      threshold: 'Usage threshold',
      selectGroup: 'Select an OpenAI group'
    },
    actions: {
      runNow: 'Scan now',
      resolve: 'Mark resolved',
      exportNotes: 'Export anomaly account notes',
      exportingNotes: 'Exporting'
    },
    runtime: {
      running: 'Task running',
      waiting: 'Waiting for next run',
      disabled: 'Automatic task disabled',
      lastRun: 'Last run',
      trigger: 'Trigger',
      nextRun: 'Next run',
      cooldown: '{count} cooling down'
    },
    trigger: {
      manual: 'Manual',
      scheduled: 'Scheduled'
    },
    summary: {
      scanned: 'Scanned',
      eligible: 'Eligible',
      atLimit: 'At threshold',
      reset: 'Reset',
      unauthorized: '401 errors',
      noCredits: 'No credits',
      failed: 'Failed',
      skipped: 'Skipped'
    },
    filters: {
      searchPlaceholder: 'Search email, account name, or ID'
    },
    columns: {
      email: 'Email account',
      group: 'Group',
      stage: 'Failure stage',
      httpStatus: 'HTTP status',
      firstDetected: 'First detected',
      lastDetected: 'Last detected',
      count: 'Count',
      status: 'Status',
      lastError: 'Latest error',
      actions: 'Actions'
    },
    status: {
      open: 'Open',
      resolved: 'Resolved',
      all: 'All statuses'
    },
    stage: {
      query: 'Usage query',
      credits: 'Credit query',
      reset: 'Window reset'
    },
    empty: 'No accounts currently have a 401 anomaly',
    runConfirm: {
      title: 'Confirm scan',
      message: 'The scan automatically consumes one reset credit when an account reaches the configured threshold and has a credit available. Continue?'
    },
    messages: {
      loadOverviewFailed: 'Failed to load automation status',
      loadGroupsFailed: 'Failed to load OpenAI groups',
      loadAnomaliesFailed: 'Failed to load 401 anomalies',
      configSaved: 'Automation settings saved',
      saveConfigFailed: 'Failed to save automation settings',
      runStarted: 'Scan started',
      runCompleted: 'Scan completed',
      runBusy: 'A scan is already running',
      runFailed: 'Failed to start scan',
      resolved: 'Anomaly marked as resolved',
      resolveFailed: 'Failed to resolve anomaly',
      noAccountNotesToExport: 'No anomaly accounts have notes to export',
      accountNotesExported: 'Exported notes for {count} anomaly accounts',
      exportAccountNotesFailed: 'Failed to export anomaly account notes'
    }
  }
}
