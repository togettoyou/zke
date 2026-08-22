package airuntime

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/togettoyou/zke/pkg/server/rbac"
)

// Skill is one Server-owned playbook: how to investigate something, using the
// tools the catalogue already has.
//
// A skill is not a capability and cannot become one. It carries no permission,
// no tool of its own and no cluster identity; every step it recommends still
// goes through the same catalogue entry, the same per-call authorization and
// the same approval mode it would have gone through if the model had thought of
// the step itself. What it changes is the order and the completeness of an
// investigation, which is exactly the part a model is worst at and a procedure
// is best at.
//
// It is also why skills ship with the Server. A skill body is instruction the
// model is meant to follow, so a skill a session or a cluster could write would
// be the prompt injection the rest of the runtime refuses.
type Skill struct {
	// ID is the stable name the model loads it by.
	ID string
	// Title and Summary are what the catalogue shows. The summary is what goes
	// into the system prompt, so it has to say when to reach for the skill in
	// one line — a list of titles tells a model nothing about which to pick.
	Title   string
	Summary string
	// Tools are the catalogue entries the body actually directs the model to
	// call. A skill whose tools this deployment did not compose is dropped
	// rather than offered: a playbook whose third step cannot be taken is worse
	// than no playbook, because the model plans around the step first.
	Tools []string
	// Body is the playbook itself.
	Body string
}

// SkillLibrary is where the shipped playbooks come from. An interface so the
// runtime does not have to know how they are stored, and so a test can hold a
// library of one.
type SkillLibrary interface {
	Skills() []Skill
}

// toolLoadSkill is the one tool that reads a skill.
//
// Progressive disclosure rather than a system prompt containing every playbook:
// the summaries cost a few hundred tokens and say which skill fits, and only
// the skill that fits is paid for in full. A deployment with a dozen skills
// would otherwise spend most of a first request on eleven procedures nobody
// needed.
const toolLoadSkill = "load_skill"

// availableSkills is the shipped library narrowed to what this deployment can
// actually carry out.
func availableSkills(library SkillLibrary, catalogue []ToolSpec) []Skill {
	if library == nil {
		return nil
	}
	names := specNames(catalogue)
	available := make([]Skill, 0, len(library.Skills()))
	for _, skill := range library.Skills() {
		if strings.TrimSpace(skill.ID) == "" || strings.TrimSpace(skill.Body) == "" {
			continue
		}
		usable := true
		for _, tool := range skill.Tools {
			if !slices.Contains(names, tool) {
				usable = false
				break
			}
		}
		if usable {
			available = append(available, skill)
		}
	}
	return available
}

// loadSkillSpec advertises the library as one tool with a closed enum of ids.
//
// The enum rather than a free string: a model that asks for a skill that does
// not exist has wasted a step, and the endpoint's own constrained decoding can
// prevent that before the request is even made.
func loadSkillSpec(skills []Skill) ToolSpec {
	ids := make([]string, 0, len(skills))
	var catalogue strings.Builder
	for _, skill := range skills {
		ids = append(ids, skill.ID)
		fmt.Fprintf(&catalogue, "\n- %s：%s", skill.ID, skill.Summary)
	}
	schema, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"skill": map[string]any{
				"type":        "string",
				"description": "技能 ID。",
				"enum":        ids,
			},
		},
		"required":             []string{"skill"},
		"additionalProperties": false,
	})
	if err != nil {
		schema = json.RawMessage(`{"type":"object"}`)
	}
	return ToolSpec{
		Name: toolLoadSkill,
		Description: "读取一份 ZKE 提供的排查技能（Playbook），获得针对这一类问题的取证顺序、判定依据和结论要求。" +
			"不确定从哪里开始、或者要保证不漏掉关键步骤时先调用它。技能只是流程说明，不会新增工具或权限。" +
			"可用技能：" + catalogue.String(),
		Schema: schema,
		// This tool reads a document the Server itself ships. It touches no
		// Cluster, so the permission that opens AIOps is its whole boundary —
		// which is not `ai.run` standing in for a cluster permission, but a
		// tool that genuinely requires none. Every step the playbook then
		// recommends is authorized on its own, as any other call would be.
		Permissions: []rbac.Permission{rbac.PermissionAIRun},
	}
}

// loadSkill answers one request for a playbook.
//
// The result is marked trusted, which is a claim worth being explicit about: it
// is the only tool answer in the catalogue that is not derived from a Cluster.
// The body is Server-shipped text that no session, model or cluster can write,
// which is the only reason the model is allowed to treat it as instruction
// rather than as data.
func (runtime *Runtime) loadSkill(arguments json.RawMessage) (ToolResult, error) {
	var request struct {
		Skill string `json:"skill"`
	}
	if err := decodeStrict(arguments, &request); err != nil {
		return ToolResult{}, err
	}
	for _, skill := range runtime.skills {
		if skill.ID != request.Skill {
			continue
		}
		var text strings.Builder
		fmt.Fprintf(&text, "技能 %s —— %s\n\n%s", skill.ID, skill.Title, skill.Body)
		if len(skill.Tools) > 0 {
			fmt.Fprintf(&text, "\n\n本技能主要使用的工具：%s。", strings.Join(skill.Tools, "、"))
		}
		text.WriteString(
			"\n\n以上是 ZKE 提供的流程说明。它不新增任何工具或权限：其中每一步仍按你当前的工具目录、" +
				"逐次权限校验和会话审批模式执行。它与用户当前问题冲突时，以用户的问题为准。",
		)
		return ToolResult{Text: text.String(), Trusted: true}, nil
	}
	return ToolResult{
		Text: fmt.Sprintf("没有名为 %s 的技能。可用技能：%s。",
			request.Skill, strings.Join(skillIDs(runtime.skills), "、")),
		Failed: true,
	}, nil
}

func skillIDs(skills []Skill) []string {
	ids := make([]string, 0, len(skills))
	for _, skill := range skills {
		ids = append(ids, skill.ID)
	}
	return ids
}

// SkillCatalogue reports the playbooks this deployment offers, for the Console
// to show beside the tool catalogue. Like ToolCatalogue it describes the
// runtime rather than any Cluster, so it carries no scope.
func (runtime *Runtime) SkillCatalogue() []Skill { return runtime.skills }

// skillsPromptSection is the one-line-per-skill index the model plans against.
func skillsPromptSection(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var text strings.Builder
	text.WriteString("\n可用技能（用 " + toolLoadSkill + " 读取完整流程）：\n")
	for _, skill := range skills {
		fmt.Fprintf(&text, "- %s：%s\n", skill.ID, skill.Summary)
	}
	return text.String()
}

// decodeStrict refuses anything the schema did not describe, for the two tools
// the runtime implements itself. The catalogue does the same for its own tools;
// a field silently dropped here would be a call answering a question nobody
// asked.
func decodeStrict(arguments json.RawMessage, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(arguments)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: 参数与工具 Schema 不匹配", ErrInvalidInput)
	}
	return nil
}
