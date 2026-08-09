export default {
  accountHealthDetector: {
    title: '账号健康检测',
    description: '选择 OpenAI 分组后，读取其中已有账号，并根据账号备注检测订阅与封禁证据',
    groups: {
      title: '选择检测分组',
      selectedCount: '已选择 {count} 个分组',
      searchPlaceholder: '搜索分组名称或 ID',
      searchLabel: '搜索可检测分组',
      empty: '没有符合条件的 OpenAI 分组'
    },
    controls: {
      concurrency: '并发数'
    },
    actions: {
      loadAccounts: '加载账号',
      scanAccounts: '检测全部账号（{count}）',
      stop: '停止',
      reloadGroups: '刷新分组',
      selectVisibleGroups: '选择当前分组',
      clearGroups: '清空分组',
      selectFilteredAccounts: '选择筛选账号',
      clearAccountSelection: '清空账号选择',
      exportSelectedNotes: '导出所选备注（{count}）',
      deleteSelected: '删除选中（{count}）',
      deleteAll: '一键删除全部（{count}）',
      deleteAccount: '删除账号 {name}'
    },
    progress: {
      label: '检测进度'
    },
    summary: {
      selectedGroups: '已选分组',
      loadedAccounts: '账号数',
      selectedAccounts: '已选账号',
      checked: '已检测',
      plus: 'Plus 有效',
      banned: '已封禁',
      warning: '风险警告',
      formatErrors: '格式问题',
      failed: '异常或停止'
    },
    filters: {
      searchPlaceholder: '搜索账号、分组、邮箱、证据或 ID',
      searchLabel: '搜索账号检测结果',
      localStatusLabel: '按账号状态筛选',
      outcomeLabel: '按检测结果筛选',
      banStatusLabel: '按封禁状态筛选',
      plusStatusLabel: '按 Plus 状态筛选',
      sortFieldLabel: '排序字段',
      sortAscending: '当前为升序，点击切换为降序',
      sortDescending: '当前为降序，点击切换为升序',
      allLocalStatuses: '全部账号状态',
      allOutcomes: '全部检测结果',
      allBanStatuses: '全部封禁状态',
      allPlusStatuses: '全部 Plus 状态',
      visibleCount: '当前显示 {count} 个账号'
    },
    columns: {
      account: '账号',
      groups: '所属分组',
      accountStatus: '账号状态',
      outcome: '检测结果',
      banStatus: '封禁状态',
      plusStatus: 'Plus 状态',
      plusDate: 'Plus 邮件日期',
      paymentMethod: '付款方式',
      banDate: '封禁邮件日期',
      lifespan: '存活时长',
      score: '证据分',
      messagePageCount: '邮件 / 页',
      evidence: '命中证据',
      elapsed: '耗时',
      checkedAt: '检测时间'
    },
    localStatus: {
      active: '启用',
      inactive: '停用',
      error: '异常'
    },
    outcome: {
      idle: '未检测',
      queued: '等待中',
      checking: '检测中',
      deactivated: '已封禁',
      warning: '风险警告',
      reactivated: '已恢复',
      no_evidence: '未发现封禁证据',
      mismatch: '账号不匹配',
      fetch_error: '查询失败',
      parse_error: '解析失败',
      format_error: '格式问题，检测失败',
      stopped: '已停止',
      failed: '检测失败'
    },
    banStatus: {
      unknown: '待检测',
      not_banned: '未发现封禁',
      banned: '已封禁',
      reactivated: '已恢复',
      warning: '风险警告',
      mismatch: '账号不匹配'
    },
    plusStatus: {
      unknown: '待检测',
      not_detected: '未检测到',
      detected: 'Plus 有效',
      detectedWithScore: 'Plus 有效 · {score}分',
      banned: '已封禁'
    },
    lifespan: {
      active: '存活中 {days}天{hours}小时',
      ended: '{days}天{hours}小时',
      banDateUnknown: '封禁日期未知',
      invalidDate: '日期异常'
    },
    evidence: {
      account_mismatch: '正文账号与检测账号不一致',
      plus_after_deactivation: 'Plus 订阅邮件晚于历史封禁邮件',
      dynamic_mailbox: '页面没有可识别邮件，可能由 JavaScript 动态加载',
      reactivated_notice: '匹配账号恢复通知',
      deactivation_semantics: '账号已停用语义',
      openai_anchor: 'OpenAI 标识',
      account_unavailable: '账号不可继续使用',
      policy_violation: '条款/政策违规',
      appeal_available: '申诉入口',
      deactivation_subject: '匹配封禁邮件标题模板',
      warning_notice: '匹配账号警告通知',
      plus_marker: 'ChatGPT Plus 标识',
      subscription_confirmed: '订阅成功语义',
      order_number: '订单编号',
      payment_method: '付款方式',
      order_date: '订单日期',
      subscription_management: '订阅管理/续费说明'
    },
    formatIssue: {
      empty: '账号备注为空',
      missing_email: '账号备注中未找到邮箱地址',
      missing_url: '账号备注中未找到 HTTP(S) 检测链接',
      multiple_records: '账号备注中包含多个账号记录',
      invalid_url: '账号备注中的检测链接无效',
      mixed_invalid_lines: '账号备注中同时包含有效记录和无效行'
    },
    values: {
      score: '{value}分',
      messageDate: '证据邮件日期：{value}',
      associatedEmail: '实际关联邮箱：{value}'
    },
    reasons: {
      requestFailed: '无法完成检测请求'
    },
    selectionLabel: '选择账号 {name}',
    empty: {
      selectGroups: '请先选择需要检测的分组',
      loadAccounts: '分组已选择，请加载账号',
      noAccounts: '所选分组中没有 OpenAI 账号'
    },
    deleteDialog: {
      title: '删除账号',
      single: '确定删除账号“{name}”吗？删除后无法恢复。',
      batch: '确定删除这 {count} 个账号吗？删除后无法恢复。'
    },
    messages: {
      loadGroupsFailed: '加载 OpenAI 分组失败',
      loadAccountsFailed: '加载所选分组账号失败',
      noGroupSelection: '请至少选择一个分组',
      noAccounts: '当前没有可检测账号',
      groupLimit: '一次最多选择 {count} 个分组',
      accountNoteExportLimit: '每次最多导出 {count} 个账号的备注',
      accountNotesExported: '已导出 {count} 个账号的备注',
      exportAccountNotesFailed: '导出所选账号备注失败',
      deleteSuccess: '已删除 {count} 个账号',
      deletePartial: '删除完成：成功 {success} 个，失败 {failed} 个',
      deleteFailed: '删除账号失败'
    }
  }
}
