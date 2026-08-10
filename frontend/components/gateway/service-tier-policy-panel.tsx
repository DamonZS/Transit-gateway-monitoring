import { useState } from "react"
import { ChevronDown, Filter, KeyRound, Plus, Save, Trash2, Zap } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"
import type {
  GatewayKey,
  GatewayServiceTierAction,
  GatewayServiceTierRule,
} from "@/lib/api-types"

type ServiceTierPolicyPanelProps = {
  rules: GatewayServiceTierRule[]
  keys: GatewayKey[]
  busy: boolean
  onChange: (rules: GatewayServiceTierRule[]) => void
  onSave: () => void
}

const tierOptions = [
  { value: "all", label: "全部 tier", description: "匹配任意 service_tier" },
  { value: "priority", label: "priority（fast）", description: "仅匹配 priority / fast" },
  { value: "flex", label: "flex", description: "仅匹配 flex" },
] as const

function selectedKeyIDs(rule: GatewayServiceTierRule) {
  return [...new Set((rule.key_ids ?? []).filter((keyID) => Number.isInteger(keyID) && keyID > 0))]
}

export function ServiceTierPolicyPanel({
  rules,
  keys,
  busy,
  onChange,
  onSave,
}: ServiceTierPolicyPanelProps) {
  const [modelDrafts, setModelDrafts] = useState<Record<number, string>>({})

  function updateRule(index: number, patch: Partial<GatewayServiceTierRule>) {
    onChange(rules.map((rule, i) => (i === index ? { ...rule, ...patch } : rule)))
  }

  function addModel(index: number) {
    const model = modelDrafts[index]?.trim()
    if (!model) return
    const existing = rules[index]?.models ?? []
    if (!existing.some((item) => item.toLowerCase() === model.toLowerCase())) {
      updateRule(index, { models: [...existing, model] })
    }
    setModelDrafts((prev) => ({ ...prev, [index]: "" }))
  }

  function removeModel(index: number, modelIndex: number) {
    updateRule(index, { models: (rules[index]?.models ?? []).filter((_, i) => i !== modelIndex) })
  }

  function updateSelectedKeys(index: number, keyIDs: number[]) {
    updateRule(index, { key_scope: "selected", key_ids: keyIDs, key_id: undefined, user_email: undefined })
  }

  return (
    <div className="border-border bg-card overflow-hidden rounded-lg border shadow-none">
      <div className="flex flex-wrap items-start justify-between gap-3 border-b border-border px-4 py-4 sm:px-5">
        <div className="min-w-0">
          <div className="flex items-center gap-2 text-sm font-medium">
            <Zap className="size-4 text-amber-600 dark:text-amber-400" />
            OpenAI Fast / Flex 策略
          </div>
          <p className="mt-1 text-xs leading-5 text-muted-foreground">
            仅处理 OpenAI Chat 与 Responses 请求。指定 Key 规则优先于全部 Key 规则。
          </p>
        </div>
        <div className="inline-flex shrink-0 items-center gap-1 rounded-md border border-border bg-muted/35 px-2 py-1 text-[11px] text-muted-foreground">
          <Filter className="size-3" /> service_tier
        </div>
      </div>

      <div className="space-y-3 p-4 sm:p-5">
        {rules.map((rule, index) => {
          const selectedKeys = selectedKeyIDs(rule)
          const scopedKey = rule.key_scope === "selected" || selectedKeys.length > 0 || Number(rule.key_id) > 0 || !!rule.user_email
          return (
            <section key={index} className="rounded-lg border border-border bg-background p-3 sm:p-4">
              <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-2">
                  <span className="inline-flex size-6 items-center justify-center rounded-md bg-primary/10 text-xs font-semibold text-primary">
                    {index + 1}
                  </span>
                  <span className="text-sm font-medium">规则 #{index + 1}</span>
                </div>
                <Button
                  type="button"
                  size="icon-sm"
                  variant="ghost"
                  disabled={busy}
                  title="删除规则"
                  onClick={() => onChange(rules.filter((_, i) => i !== index))}
                >
                  <Trash2 className="size-3.5 text-muted-foreground" />
                </Button>
              </div>

              <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,1.35fr)]">
                <div className="space-y-1.5">
                  <Label>service_tier 匹配</Label>
                  <Select
                    value={rule.tier}
                    onValueChange={(tier) =>
                      updateRule(index, { tier: tier as GatewayServiceTierRule["tier"] })
                    }
                    disabled={busy}
                  >
                    <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      {tierOptions.map((option) => (
                        <SelectItem key={option.value} value={option.value} description={option.description}>
                          {option.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>

                <div className="space-y-1.5">
                  <Label>处理方式</Label>
                  <Select
                    value={rule.action}
                    onValueChange={(action) => updateRule(index, { action: action as GatewayServiceTierAction })}
                    disabled={busy}
                  >
                    <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value="passthrough" description="保留 service_tier；fast 会规范为 priority">透传</SelectItem>
                      <SelectItem value="filter" description="移除请求体中的 service_tier">过滤</SelectItem>
                      <SelectItem value="block" description="直接拒绝请求，不转发至上游">阻断</SelectItem>
                      <SelectItem value="force_priority" description="将匹配的 service_tier 改写为 priority">强制 priority</SelectItem>
                    </SelectContent>
                  </Select>
                </div>

                <div className="space-y-1.5">
                  <Label>生效范围</Label>
                  <div className="flex h-9 rounded-md border border-input bg-muted/25 p-0.5">
                    <button
                      type="button"
                      disabled={busy}
                      onClick={() => updateRule(index, { key_scope: "all", key_ids: undefined, key_id: undefined, user_email: undefined })}
                      className={cn(
                        "flex min-w-0 flex-1 items-center justify-center rounded px-2 text-xs font-medium transition-colors",
                        !scopedKey ? "bg-background text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground",
                      )}
                    >
                      全部 Key
                    </button>
                    <button
                      type="button"
                      disabled={busy || keys.length === 0}
                      title={keys.length === 0 ? "请先创建 API Key" : undefined}
                      onClick={() => updateSelectedKeys(index, selectedKeys.length > 0 ? selectedKeys : [keys[0].id])}
                      className={cn(
                        "flex min-w-0 flex-1 items-center justify-center gap-1 rounded px-2 text-xs font-medium transition-colors",
                        scopedKey ? "bg-background text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground",
                      )}
                    >
                      <KeyRound className="size-3" /> 指定 Key
                    </button>
                  </div>
                  {scopedKey ? (
                    <Popover>
                      <PopoverTrigger asChild>
                        <Button
                          type="button"
                          variant="outline"
                          className="w-full justify-between font-normal"
                          disabled={busy || keys.length === 0}
                        >
                          <span className="flex min-w-0 items-center gap-2 truncate">
                            <KeyRound className="size-3.5 shrink-0 text-muted-foreground" />
                            {selectedKeys.length > 0 ? `已选 ${selectedKeys.length} 个 Key` : "选择 API Key"}
                          </span>
                          <ChevronDown className="size-4 shrink-0 opacity-50" />
                        </Button>
                      </PopoverTrigger>
                      <PopoverContent align="start" className="w-[var(--radix-popover-trigger-width)] p-2">
                        <div className="flex items-center justify-between gap-2 px-1 pb-2 text-xs">
                          <span className="text-muted-foreground">选择生效的 API Key</span>
                          <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            className="h-6 px-1.5 text-xs"
                            disabled={busy}
                            onClick={() => updateSelectedKeys(
                              index,
                              selectedKeys.length === 0 ? keys.map((key) => key.id) : [],
                            )}
                          >
                            {selectedKeys.length === 0 ? "全选" : "取消全选"}
                          </Button>
                        </div>
                        <div className="max-h-56 space-y-1 overflow-y-auto">
                          {keys.map((key) => {
                            const checked = selectedKeys.includes(key.id)
                            return (
                              <label
                                key={key.id}
                                className="flex cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-sm hover:bg-muted"
                              >
                                <Checkbox
                                  checked={checked}
                                  disabled={busy}
                                  onCheckedChange={(next) => {
                                    if (next === true) {
                                      updateSelectedKeys(index, [...selectedKeys, key.id])
                                    } else {
                                      updateSelectedKeys(index, selectedKeys.filter((keyID) => keyID !== key.id))
                                    }
                                  }}
                                />
                                <span className="min-w-0 flex-1 truncate">
                                  {key.name} <span className="font-mono text-[11px] text-muted-foreground">({key.key_prefix})</span>
                                </span>
                              </label>
                            )
                          })}
                        </div>
                      </PopoverContent>
                    </Popover>
                  ) : null}
                </div>
              </div>

              <div className="mt-3 border-t border-border/70 pt-3">
                <Label>模型白名单</Label>
                <p className="mt-1 text-[11px] leading-4 text-muted-foreground">
                  留空表示所有模型；支持精确匹配与通配符，例如 <code>gpt-5.5*</code>。
                </p>
                <div className="mt-2 flex flex-wrap gap-1.5">
                  {(rule.models ?? []).map((model, modelIndex) => (
                    <span key={`${model}-${modelIndex}`} className="inline-flex h-7 max-w-full items-center gap-1 rounded-md border border-border bg-muted/35 py-1 pr-1 pl-2 font-mono text-[11px]">
                      <span className="truncate">{model}</span>
                      <button
                        type="button"
                        title={`移除 ${model}`}
                        disabled={busy}
                        onClick={() => removeModel(index, modelIndex)}
                        className="inline-flex size-5 shrink-0 items-center justify-center rounded-sm text-muted-foreground hover:bg-background hover:text-foreground"
                      >
                        <Trash2 className="size-3" />
                      </button>
                    </span>
                  ))}
                </div>
                <div className="mt-2 flex gap-2">
                  <Input
                    value={modelDrafts[index] ?? ""}
                    placeholder="添加模型规则"
                    disabled={busy}
                    onChange={(event) => setModelDrafts((prev) => ({ ...prev, [index]: event.target.value }))}
                    onKeyDown={(event) => {
                      if (event.key === "Enter") {
                        event.preventDefault()
                        addModel(index)
                      }
                    }}
                  />
                  <Button type="button" size="icon" variant="outline" disabled={busy} title="添加模型规则" onClick={() => addModel(index)}>
                    <Plus className="size-4" />
                  </Button>
                </div>
              </div>
            </section>
          )
        })}

        {rules.length === 0 ? (
          <div className="rounded-md border border-dashed border-border px-4 py-8 text-center text-sm text-muted-foreground">
            暂无规则，添加后才会处理 service_tier。
          </div>
        ) : null}

        <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border pt-3">
          <Button
            type="button"
            variant="outline"
            disabled={busy}
            onClick={() => onChange([...rules, { tier: "all", action: "filter", models: [] }])}
          >
            <Plus className="size-3.5" /> 新增规则
          </Button>
          <Button type="button" disabled={busy} onClick={onSave}>
            <Save className="size-3.5" /> 保存策略
          </Button>
        </div>
      </div>
    </div>
  )
}
