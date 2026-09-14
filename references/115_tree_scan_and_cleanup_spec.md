# 115 扫描、番号匹配与清理适配规范

本文件定义新服务算法及上游字段适配。生命周期以[主设计§8](../docs/PROJECT_SPEC_GO_VIRTUAL_MEDIA_SERVER.md#8-115-资源与任务生命周期)为权威，字段原始证据见[115字段参考](./115_openapi_field_spec.md)，当前能力须通过[G0](../specs/mediavault-server/phases/00-integration-gate.md)。

## 1. 扫描范围、分页与对账

文件列表候选接口为 GET /open/ufile/files。历史记录表明 type=4&cur=0 返回子树视频，limit=1150可用，包含fid/fn/pid/pc/fs/fc/upt等；不带type时cur=0不等于递归。目录信息须逐层列举或按父ID查路径。上述数值和字段在P2必须用当前fixture验证，不能混用Cookie返回结构。

完整扫描支持根部散放视频及任意层级分类目录：先分页取得所有项，对目录继续下钻，按binding+file_id去重；文件名识别不到番号时，依次查最近祖先目录。文件与目录识别为不同影片则记conflict并跳过自动绑定。影片不存在时只记录未匹配，不自动创建影片。

每个根独立scan_runs和scan_seen。所有分页/子目录成功后才完成一次full；只有full completed才可淘汰该根未看到的旧绑定。根配置取消是解除扫描范围，不能当作云文件删除。重叠根的同一文件不能重复建资产，若另一个有效范围仍看到该文件不能误标missing。

upt增量只加速新增/修改，不检测全部移动；每24h至少一次完整扫描，不根据count相同跳过。任意页超时/限流/异常为空都使本轮failed，不触发缺失淘汰。分页期间发现重复/总数漂移无法确认快照时，安排完整重扫或对缺失文件逐个验证后才标missing。

type=4可能包含ISO。新服务仅接受mp4/mkv/ts正片，剔除sample/预告、<100MiB和不支持封装。识别到CD1/CD2、part1/part2等分段时记录unsupported_multi_part，不用“取最大文件”假装整片完整。单片多版本目录分别保留；同一版本目录中选最大有效正片。

## 2. 番号归一化

统一大写，标准形式PREFIX-NUMBER，FC2统一FC2-PPV-NUMBER；数字保留前导零。仅接受有明确边界的候选，下划线可作为标签边界。先识别FC2和日期型，再识别标准型，噪声候选被排除后继续寻找。发现不同的多个合法番号时返回歧义，不自动取第一个。以下Go参考只规定此支持集；扩展新格式需补样例，不能放宽为任意字母数字贪婪串。

```go
package identity

import (
    "regexp"
    "strings"
)

type span struct { start, end int }
var fc2 = regexp.MustCompile(`(?i)FC2(?:[-_ ]?PPV)?[-_ ]?([0-9]{5,8})`)
var dated = regexp.MustCompile(`(?i)(?:CARIB[-_ ]?)?([0-9]{6})[-_]([0-9]{3})`)
var standard = regexp.MustCompile(`(?i)([0-9]{1,3}[A-Z]{2,10}|[A-Z]{2,10})[-_ ]?([0-9]{2,8})`)

func asciiAlnum(b byte) bool {
    return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}
func boundary(s string, a, b int) bool {
    return (a == 0 || !asciiAlnum(s[a-1])) && (b == len(s) || !asciiAlnum(s[b]))
}
func ExtractCodeFromFolderName(name string) string {
    found := map[string]bool{}
    reserved := []span{}
    add := func(m []int, code string) {
        if boundary(name, m[0], m[1]) {
            found[code] = true
            reserved = append(reserved, span{m[0], m[1]})
        }
    }
    for _, m := range fc2.FindAllStringSubmatchIndex(name, -1) {
        add(m, "FC2-PPV-" + name[m[2]:m[3]])
    }
    for _, m := range dated.FindAllStringSubmatchIndex(name, -1) {
        add(m, name[m[2]:m[3]] + "-" + name[m[4]:m[5]])
    }
    noise := map[string]bool{"HD":true, "FHD":true, "UHD":true,
        "HEVC":true, "AVC":true, "H264":true, "H265":true}
    for _, m := range standard.FindAllStringSubmatchIndex(name, -1) {
        overlap := false
        for _, r := range reserved {
            if m[0] < r.end && m[1] > r.start { overlap = true; break }
        }
        prefix := strings.ToUpper(name[m[2]:m[3]])
        if !overlap && !noise[prefix] && boundary(name, m[0], m[1]) {
            found[prefix + "-" + name[m[4]:m[5]]] = true
        }
    }
    if len(found) != 1 { return "" }
    for code := range found { return code }
    return ""
}
```

样例包含IPX-123、IPX123、SSNI-999_CH_HD、H264-1080 IPX-123、FC2PPV1234567、259LUXU-1234、carib-010124-001及歧义输入，预期结果见[共用样例](./resource_identity_examples.json)。空结果须在调用层区分none/ambiguous供日志诊断，不等于影片不存在。

## 3. 永久映射

existing资源key为115:<binding UUID>:<file_id>，pick_code只是可更新取链字段。已有影片、资源和permanent ready资产同事务upsert；不赋高分，不在逻辑版本表存可播布尔。失效只在完整对账或明确not_found之后标missing，暂时调用失败保留身份。

## 4. 清理协议

候选删除接口POST /open/ufile/delete；file_ids字段及原始返回需G0固定。state=true/HTTP200最多说明请求被接受，不能说明已物理清除。父链、独占目录所有权、有效播放租约、代次均需再次核验。

资产保存owned_root_id、root_snapshot、owning_job_id、manifest_json。只删除本任务拥有的子项；不删除配置根，不删除permanent资产，不自动清空整个回收站。目录被移动或出现无法证明归属的新文件时quarantined并告警。

pending_delete→deleting→deleted，以再次查询确认消失为完成。1～2秒是最短轮询间隔，10分钟仍不确定则保留reconcile任务；目标ID不能清空。TTL不延长已有临时资产；cleanup_enabled=false只停删除，已过期资产仍不接新播放。

## 5. 恢复与验收

旧清理只能更新对应asset_id/generation；重转存建立新资产。删除影片使用软删除并安排资产回收，hard delete后清理记录仍保留。任何接口错误不能只看errno!=0就判死文件，必须按G0错误分类处理。

必须覆盖：多层/根部/分段/ISO；分页中途失败；同count移动；绑定重复；根重叠/资产移动；播放租约到期；删除已接受但未确认；服务重启；旧清理与新转存并发。对账失败不扩大删除范围。
