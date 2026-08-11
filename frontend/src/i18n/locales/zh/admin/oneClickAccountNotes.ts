export default {
  oneClickAccountNotes: {
    title: '一键备注',
    description: '上传固定格式的 TXT 文档，预览邮箱匹配结果，并将每一行原文写入对应账号备注',
    upload: {
      title: '上传备注文件',
      description: '每个非空行必须包含需要更新账号的邮箱地址。',
      dropOrChoose: '将 TXT 文件拖到此处，或选择文件',
      chooseFile: '选择 TXT 文件',
      replaceFile: '更换文件',
      removeFile: '移除文件',
      selectedFile: '已选择文件',
      formatHint: '仅支持 TXT；匹配成功后会将整行原文保存为账号备注。',
      reading: '正在读取文件...',
      lineCount: '共 {count} 个非空行'
    },
    preview: {
      title: '匹配预览',
      description: '覆盖备注前，请确认邮箱与账号的匹配结果。',
      empty: '上传 TXT 文件后即可预览账号匹配结果。',
      loading: '正在匹配账号...',
      privacy: '原始行可能包含敏感凭据，因此不会在预览中显示。'
    },
    summary: {
      totalLines: '总行数',
      validLines: '有效邮箱',
      matchedLines: '匹配行',
      matchedAccounts: '匹配账号',
      willUpdateAccounts: '将更新',
      unchangedAccounts: '无需更新',
      unmatchedLines: '未匹配',
      duplicateLines: '重复行',
      conflictLines: '冲突行',
      invalidLines: '无效行',
      failedAccounts: '失败账号'
    },
    columns: {
      line: '行号',
      email: '邮箱',
      matchedAccounts: '匹配账号',
      changes: '变更',
      status: '状态',
      details: '详情'
    },
    statuses: {
      matched: '已匹配',
      unmatched: '未匹配账号',
      invalid: '未找到有效邮箱',
      duplicate: '重复行',
      conflict: '重复内容冲突',
      multiple_matches: '匹配多个账号',
      ready: '等待应用',
      updated: '已更新',
      skipped: '已跳过',
      failed: '失败'
    },
    details: {
      matchedAccounts: '匹配到 {count} 个账号',
      willUpdateAccounts: '将更新 {count} 个账号',
      unchangedAccounts: '{count} 个账号无需更新',
      duplicateOfLine: '与第 {line} 行相同'
    },
    reasons: {
      missingEmail: '该行未找到邮箱地址',
      duplicateIdenticalLine: '该行与前面的行重复',
      duplicateEmailConflict: '同一邮箱对应了不同的备注内容'
    },
    actions: {
      preview: '预览匹配',
      previewing: '正在预览...',
      apply: '应用备注',
      applying: '正在应用...',
      chooseAnother: '选择其他文件',
      retry: '重试'
    },
    confirm: {
      title: '覆盖匹配账号的备注？',
      message: '将使用对应整行原文覆盖 {count} 个匹配账号的现有备注。',
      warning: '此页面无法撤销该操作，请确认预览结果后再继续。',
      cancel: '取消',
      apply: '覆盖备注'
    },
    result: {
      title: '应用结果',
      success: '已更新 {count} 个账号的备注。',
      partial: '已更新 {updated} 个账号，{failed} 个失败。',
      noChanges: '没有账号备注被修改。',
      unprocessedSummary: '未处理：未匹配 {unmatched} 行、无效 {invalid} 行、冲突 {conflicts} 行。',
      updated: '已更新',
      unchanged: '无需更新',
      skipped: '已跳过',
      failed: '失败'
    },
    errors: {
      singleFileOnly: '每次只能选择一个 TXT 文件。',
      invalidFileType: '请选择 .txt 文件。',
      emptyFile: 'TXT 文件中没有非空行。',
      fileTooLarge: 'TXT 文件超过允许大小。',
      readFailed: '无法读取 TXT 文件。',
      noValidLines: '文件中没有包含有效邮箱的行。',
      noMatchedAccounts: '文件中的邮箱均未匹配到账号。',
      previewFailed: '账号匹配失败，请重试。',
      busy: '当前已有一键备注任务正在处理，请稍后重试。',
      planTooLarge: '匹配账号或备注数据量过大，请拆分文件后重试。',
      stalePreview: '当前预览已失效，请重新预览后再应用。',
      applyFailed: '无法更新账号备注。',
      unknown: '操作失败，请重试。',

      // 预览和应用接口返回的稳定 reason 错误码。
      ACCOUNT_NOTE_IMPORT_BUSY: '备注导入服务繁忙，请稍后重试。',
      ACCOUNT_NOTE_IMPORT_FILE_EMPTY: 'TXT 文件为空。',
      ACCOUNT_NOTE_IMPORT_FILE_INVALID: '请准确上传一个 TXT 文件。',
      ACCOUNT_NOTE_IMPORT_FILE_REQUIRED: '请选择要上传的 TXT 文件。',
      ACCOUNT_NOTE_IMPORT_FILE_TOO_LARGE: 'TXT 文件超过 1 MiB 大小限制。',
      ACCOUNT_NOTE_IMPORT_FILE_TYPE_INVALID: '上传文件必须使用 .txt 扩展名。',
      ACCOUNT_NOTE_IMPORT_INVALID_BOM: '文件只能在开头包含一个 UTF-8 BOM。',
      ACCOUNT_NOTE_IMPORT_INVALID_LINE_ENDING: '文件必须使用 LF 或 CRLF 换行符。',
      ACCOUNT_NOTE_IMPORT_INVALID_UTF8: '文件必须是有效的 UTF-8 文本。',
      ACCOUNT_NOTE_IMPORT_LINE_TOO_LONG: '文件包含超过 64 KiB 的单行。',
      ACCOUNT_NOTE_IMPORT_MULTIPART_INVALID: '上传请求无效，请重新选择文件。',
      ACCOUNT_NOTE_IMPORT_MULTIPART_REQUIRED: '上传请求无效，请重新选择文件。',
      ACCOUNT_NOTE_IMPORT_NO_RECORDS: 'TXT 文件中没有非空行。',
      ACCOUNT_NOTE_IMPORT_NOT_APPLICABLE: '当前预览无法应用，请处理提示的条目后重新预览。',
      ACCOUNT_NOTE_IMPORT_NUL_BYTE: '文件不能包含 NUL 字节。',
      ACCOUNT_NOTE_IMPORT_PLAN_INVALID: '预览计划无效，请重新预览文件。',
      ACCOUNT_NOTE_IMPORT_PLAN_TOO_LARGE: '匹配账号或备注数据量过大，请拆分文件后重试。',
      ACCOUNT_NOTE_IMPORT_PREVIEW_DIGEST_INVALID: '预览确认信息无效，请重新预览文件。',
      ACCOUNT_NOTE_IMPORT_PREVIEW_DIGEST_REQUIRED: '请先预览文件，再应用备注。',
      ACCOUNT_NOTE_IMPORT_PREVIEW_STALE: '当前预览已失效，请重新预览后再应用。',
      ACCOUNT_NOTE_IMPORT_REQUEST_TOO_LARGE: '上传请求超过允许大小。',
      ACCOUNT_NOTE_IMPORT_TOO_MANY_LINES: '文件包含超过 5000 个非空行。',
      ACCOUNT_NOTE_IMPORT_UNAVAILABLE: '备注导入服务暂时不可用，请稍后重试。',
      ACCOUNT_NOTE_IMPORT_UPLOAD_TIMEOUT: '文件上传超时，请检查网络后重试。',
      IDEMPOTENCY_EXECUTOR_NIL: '备注导入服务不可用，请稍后重试。',
      IDEMPOTENCY_IN_PROGRESS: '该备注导入仍在处理中，请稍后重试。',
      IDEMPOTENCY_KEY_CONFLICT: '操作标识已用于不同内容，请重新预览文件。',
      IDEMPOTENCY_KEY_INVALID: '操作标识无效，请重新预览文件。',
      IDEMPOTENCY_KEY_REQUIRED: '缺少操作标识，请重新预览文件。',
      IDEMPOTENCY_PAYLOAD_INVALID: '导入请求无法校验，请重新预览文件。',
      IDEMPOTENCY_RETRY_BACKOFF: '请稍候再重试此次备注导入。',
      IDEMPOTENCY_SCOPE_REQUIRED: '备注导入服务不可用，请稍后重试。',
      IDEMPOTENCY_STORE_UNAVAILABLE: '备注导入服务暂时不可用，请稍后重试。'
    },
    messages: {
      previewReady: '预览完成：匹配到 {count} 个账号。',
      applySuccess: '已更新 {count} 个账号的备注。',
      applyPartial: '更新完成，其中 {failed} 个失败。'
    }
  }
}
