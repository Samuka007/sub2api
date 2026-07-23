export default {
  plusQuotaAutomation: {
    title: 'Plus 用量刷新',
    description: '定时检查 Plus 分组用量并管理 401 异常账号',
    config: {
      enabled: '自动扫描',
      group: '目标分组',
      intervalMinutes: '扫描周期（分钟）',
      threshold: '用量阈值',
      selectGroup: '请选择 OpenAI 分组'
    },
    actions: {
      runNow: '立即扫描',
      resolve: '标记已解决'
    },
    runtime: {
      running: '任务运行中',
      waiting: '等待下次执行',
      disabled: '自动任务已停用',
      lastRun: '上次执行',
      trigger: '触发方式',
      nextRun: '下次执行',
      cooldown: '冷却中 {count} 个'
    },
    trigger: {
      manual: '手动',
      scheduled: '定时'
    },
    summary: {
      scanned: '已扫描',
      eligible: '符合条件',
      atLimit: '达到阈值',
      reset: '已重置',
      unauthorized: '401 异常',
      noCredits: '无可用次数',
      failed: '失败',
      skipped: '跳过'
    },
    filters: {
      searchPlaceholder: '搜索邮箱、账号名称或 ID'
    },
    columns: {
      email: '邮箱账号',
      group: '分组',
      stage: '异常阶段',
      httpStatus: 'HTTP 状态',
      firstDetected: '首次发现',
      lastDetected: '最后发现',
      count: '次数',
      status: '处理状态',
      lastError: '最近错误',
      actions: '操作'
    },
    status: {
      open: '待处理',
      resolved: '已解决',
      all: '全部状态'
    },
    stage: {
      query: '查询用量',
      credits: '查询次数',
      reset: '重置窗口'
    },
    empty: '当前没有 401 异常账号',
    runConfirm: {
      title: '确认立即扫描',
      message: '扫描会在账号用量达到阈值且存在可用次数时自动消耗 1 次重置次数。确定继续吗？'
    },
    messages: {
      loadOverviewFailed: '加载自动刷新状态失败',
      loadGroupsFailed: '加载 OpenAI 分组失败',
      loadAnomaliesFailed: '加载 401 异常账号失败',
      configSaved: '自动刷新配置已保存',
      saveConfigFailed: '保存自动刷新配置失败',
      runStarted: '扫描任务已启动',
      runCompleted: '扫描任务已完成',
      runBusy: '已有扫描任务正在运行',
      runFailed: '启动扫描任务失败',
      resolved: '异常已标记为解决',
      resolveFailed: '解决异常失败'
    }
  }
}
