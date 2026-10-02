export default {
  oneClickAccountNotes: {
    title: 'One-click Account Notes',
    description: 'Upload a formatted TXT file, preview email matches, and apply each source line to the matching account note',
    upload: {
      title: 'Upload note file',
      description: 'Each non-empty line must contain the email address of the account to update.',
      dropOrChoose: 'Drop a TXT file here, or choose a file',
      chooseFile: 'Choose TXT file',
      replaceFile: 'Replace file',
      removeFile: 'Remove file',
      selectedFile: 'Selected file',
      formatHint: 'TXT only. The complete source line is saved as the matched account note.',
      reading: 'Reading file...',
      lineCount: '{count} non-empty lines'
    },
    preview: {
      title: 'Match preview',
      description: 'Review the email and account matches before overwriting any notes.',
      empty: 'Upload a TXT file to preview account matches.',
      loading: 'Matching accounts...',
      privacy: 'Source lines are hidden because they may contain sensitive credentials.'
    },
    summary: {
      totalLines: 'Total lines',
      validLines: 'Valid emails',
      matchedLines: 'Matched lines',
      matchedAccounts: 'Matched accounts',
      willUpdateAccounts: 'Will update',
      unchangedAccounts: 'Unchanged',
      unmatchedLines: 'Unmatched',
      duplicateLines: 'Duplicates',
      conflictLines: 'Conflicts',
      invalidLines: 'Invalid lines',
      failedAccounts: 'Failed'
    },
    columns: {
      line: 'Line',
      email: 'Email',
      matchedAccounts: 'Matched accounts',
      changes: 'Changes',
      status: 'Status',
      details: 'Details'
    },
    statuses: {
      matched: 'Matched',
      unmatched: 'No account matched',
      invalid: 'No valid email',
      duplicate: 'Duplicate line',
      conflict: 'Conflicting duplicate',
      multiple_matches: 'Multiple accounts matched',
      ready: 'Ready to apply',
      updated: 'Updated',
      skipped: 'Skipped',
      failed: 'Failed'
    },
    details: {
      matchedAccounts: '{count} accounts matched',
      willUpdateAccounts: '{count} accounts will be updated',
      unchangedAccounts: '{count} accounts already have this note',
      duplicateOfLine: 'Same as line {line}'
    },
    reasons: {
      missingEmail: 'No email address was found on this line',
      duplicateIdenticalLine: 'This line duplicates an earlier line',
      duplicateEmailConflict: 'This email appears with different note content'
    },
    actions: {
      preview: 'Preview matches',
      previewing: 'Previewing...',
      apply: 'Apply notes',
      applying: 'Applying...',
      chooseAnother: 'Choose another file',
      retry: 'Try again'
    },
    confirm: {
      title: 'Overwrite matched account notes?',
      message: 'This will replace the existing notes for {count} matched accounts with their complete source lines.',
      warning: 'This change cannot be undone from this page. Verify the preview before continuing.',
      cancel: 'Cancel',
      apply: 'Overwrite notes'
    },
    result: {
      title: 'Apply result',
      success: 'Updated notes for {count} accounts.',
      partial: 'Updated {updated} accounts; {failed} failed.',
      noChanges: 'No account notes were changed.',
      unprocessedSummary: 'Not processed: {unmatched} unmatched, {invalid} invalid, and {conflicts} conflicting lines.',
      updated: 'Updated',
      unchanged: 'Unchanged',
      skipped: 'Skipped',
      failed: 'Failed'
    },
    errors: {
      singleFileOnly: 'Choose one TXT file at a time.',
      invalidFileType: 'Choose a .txt file.',
      emptyFile: 'The TXT file has no non-empty lines.',
      fileTooLarge: 'The TXT file exceeds the allowed size.',
      readFailed: 'The TXT file could not be read.',
      noValidLines: 'No line contains a valid email address.',
      noMatchedAccounts: 'No accounts matched the emails in this file.',
      previewFailed: 'Account matching failed. Please try again.',
      busy: 'Other one-click note imports are running. Please try again shortly.',
      planTooLarge: 'The matched account or note data is too large. Split the file and try again.',
      stalePreview: 'The preview is no longer current. Preview the file again before applying.',
      applyFailed: 'The account notes could not be updated.',
      unknown: 'Something went wrong. Please try again.',

      // Stable reason codes returned by the preview/apply endpoints.
      ACCOUNT_NOTE_IMPORT_BUSY: 'The note import service is busy. Try again shortly.',
      ACCOUNT_NOTE_IMPORT_FILE_EMPTY: 'The TXT file is empty.',
      ACCOUNT_NOTE_IMPORT_FILE_INVALID: 'Upload exactly one TXT file.',
      ACCOUNT_NOTE_IMPORT_FILE_REQUIRED: 'Select a TXT file to upload.',
      ACCOUNT_NOTE_IMPORT_FILE_TOO_LARGE: 'The TXT file exceeds the 1 MiB limit.',
      ACCOUNT_NOTE_IMPORT_FILE_TYPE_INVALID: 'The uploaded file must use the .txt extension.',
      ACCOUNT_NOTE_IMPORT_INVALID_BOM: 'The file may contain only one UTF-8 BOM at the beginning.',
      ACCOUNT_NOTE_IMPORT_INVALID_LINE_ENDING: 'The file must use LF or CRLF line endings.',
      ACCOUNT_NOTE_IMPORT_INVALID_UTF8: 'The file must contain valid UTF-8 text.',
      ACCOUNT_NOTE_IMPORT_LINE_TOO_LONG: 'The file contains a line longer than 64 KiB.',
      ACCOUNT_NOTE_IMPORT_MULTIPART_INVALID: 'The upload request is invalid. Choose the file again.',
      ACCOUNT_NOTE_IMPORT_MULTIPART_REQUIRED: 'The upload request is invalid. Choose the file again.',
      ACCOUNT_NOTE_IMPORT_NO_RECORDS: 'The TXT file has no non-empty lines.',
      ACCOUNT_NOTE_IMPORT_NOT_APPLICABLE: 'This preview cannot be applied. Fix the reported entries and preview again.',
      ACCOUNT_NOTE_IMPORT_NUL_BYTE: 'The file must not contain NUL bytes.',
      ACCOUNT_NOTE_IMPORT_PLAN_INVALID: 'The preview plan is invalid. Preview the file again.',
      ACCOUNT_NOTE_IMPORT_PLAN_TOO_LARGE: 'The matched account or note data is too large. Split the file and try again.',
      ACCOUNT_NOTE_IMPORT_PREVIEW_DIGEST_INVALID: 'The preview confirmation is invalid. Preview the file again.',
      ACCOUNT_NOTE_IMPORT_PREVIEW_DIGEST_REQUIRED: 'Preview the file before applying notes.',
      ACCOUNT_NOTE_IMPORT_PREVIEW_STALE: 'The preview is no longer current. Preview the file again before applying.',
      ACCOUNT_NOTE_IMPORT_REQUEST_TOO_LARGE: 'The upload request exceeds the allowed size.',
      ACCOUNT_NOTE_IMPORT_TOO_MANY_LINES: 'The file contains more than 5,000 non-empty lines.',
      ACCOUNT_NOTE_IMPORT_UNAVAILABLE: 'The note import service is temporarily unavailable. Try again later.',
      ACCOUNT_NOTE_IMPORT_UPLOAD_TIMEOUT: 'The file upload timed out. Check your connection and try again.',
      IDEMPOTENCY_EXECUTOR_NIL: 'The note import service is unavailable. Try again later.',
      IDEMPOTENCY_IN_PROGRESS: 'This note import is still processing. Try again shortly.',
      IDEMPOTENCY_KEY_CONFLICT: 'This operation key was already used for different content. Preview the file again.',
      IDEMPOTENCY_KEY_INVALID: 'The operation key is invalid. Preview the file again.',
      IDEMPOTENCY_KEY_REQUIRED: 'The operation key is missing. Preview the file again.',
      IDEMPOTENCY_PAYLOAD_INVALID: 'The import request could not be verified. Preview the file again.',
      IDEMPOTENCY_RETRY_BACKOFF: 'Please wait before retrying this note import.',
      IDEMPOTENCY_SCOPE_REQUIRED: 'The note import service is unavailable. Try again later.',
      IDEMPOTENCY_STORE_UNAVAILABLE: 'The note import service is temporarily unavailable. Try again later.'
    },
    messages: {
      previewReady: 'Preview ready: {count} accounts matched.',
      applySuccess: 'Updated notes for {count} accounts.',
      applyPartial: 'Update completed with {failed} failures.'
    }
  }
}
