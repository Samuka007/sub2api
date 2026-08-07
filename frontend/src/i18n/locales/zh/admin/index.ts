import overview from './overview'
import channels from './channels'
import accounts from './accounts'
import resources from './resources'
import ops from './ops'
import settings from './settings'
import audit from './audit'
import promptAudit from './promptAudit'
import modelTracing from './modelTracing'
import plusQuotaAutomation from './plusQuotaAutomation'
import accountHealthDetector from './accountHealthDetector'

export default {
  ...overview,
  ...channels,
  ...accounts,
  ...resources,
  ...ops,
  ...settings,
  ...audit,
  ...promptAudit,
  ...modelTracing,
  ...plusQuotaAutomation,
  ...accountHealthDetector,
}
