// Package legado 实现 Legado 五动作契约的 DTO 与基础处理器，并承载观测的抽取层（ObserveCall）。
//
// 观测契约的正本在 docs/架构/观测契约-信封与明细字段.md（待办清单 P127①）：
// 源往信封里填什么（各动作的兼容键、error 信封的 message/reason/code）、骨架往明细里落什么、
// 哪些空值是合法状态、每一格谁读、值的样本守卫是哪条用例——都在那一页。
// **信封与明细字段增删必须同批改那一页**：模型侧由 make vet 的 observe-contract-check 钉住
// （ApiCallLog 的 json 列与 CallSubject 的字段名必须在页内）；「源填没填值」机器检不了，
// 靠用例与样本守卫。本注释只是指路，字段语义不复述在这里——第二份事实来源会漂移。
package legado
