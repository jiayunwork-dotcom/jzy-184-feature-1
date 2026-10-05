# 玉米生育期积温预测服务

给县农技站小程序用的后端：自动气象站每日最高/最低气温上报（乱序补传、
序号仲裁、更正）、地块改绑、缺测自动补值，增量维护每块地的逐日累计积温
与出苗/拔节/抽雄/吐丝/成熟日期；任何变化后的结果都与“只拿最终数据从
播种日从头算一遍”逐日一致，阶段日期变更产生可拉取的事件。

技术栈：Go 1.22 + Gin + PostgreSQL 16。设计取舍见 [docs/design.md](docs/design.md)。

## 快速开始

```bash
docker compose up -d --build
# 健康检查
curl -s localhost:8080/healthz
```

也可直接 `go run ./cmd/server`（用环境变量 `DATABASE_URL` 指向 PG 16）。
启动时自动执行数据库迁移，并对全部地块做一次全量对齐（重启续跑）。

环境变量：`DATABASE_URL`（默认 `postgres://agri:agri@localhost:5432/agristation?sslmode=disable`）、
`HTTP_ADDR`（默认 `:8080`）。

## 接口（前缀 `/api/v1`）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/stations` | 登记/修改气象站（code、name、经纬度、海拔） |
| GET | `/stations` | 站点列表 |
| POST | `/varieties` | 登记/修改品种（基点、上限、五阶段累计积温阈值） |
| POST | `/plots` | 登记/修改地块（sow_date、variety、method、初始 station） |
| GET | `/plots` | 地块列表 |
| POST | `/plots/:code/bindings` | 自某日起改绑到另一站 |
| GET | `/plots/:code/bindings` | 改绑历史 |
| POST | `/climate-normals` | 站点历年同日气候平均（doy 1..366） |
| POST | `/historical/batch` | 按站点+年份整年导入/替换历年逐日气温 |
| POST | `/observations/batch` | 批量写观测（最多 5000 条/批，逐条回报） |
| GET | `/plots/:code/daily?from=&to=&date=` | 逐日积温曲线 |
| GET | `/plots/:code/stages?date=` | 五阶段日期（reached/forecast） |
| GET | `/plots/:code/stage-ranges?quantiles=&date=` | 历年试走：年数、最早最晚、分位（含到不了）、窗口内未到年数 |
| GET | `/plots/:code/arrival?stage=&by=&date=` | 某阶段在某天或之前到达的年份比例 |
| GET | `/events?after_id=&limit=&plot=` | 按游标拉阶段变更事件 |

### 典型流程

```bash
# 1. 站点、品种
curl -s -XPOST localhost:8080/api/v1/stations -H 'Content-Type: application/json' \
  -d '{"code":"S1","name":"河东一号","latitude":30.0,"longitude":100.0,"elevation":500}'
curl -s -XPOST localhost:8080/api/v1/varieties -H 'Content-Type: application/json' \
  -d '{"code":"ZD958","name":"郑单958","base_temp":10,"upper_temp":30,
       "thresholds":[30,200,400,460,800]}'

# 2. 地块（登记时给初始绑定站；method: sine 默认 / triangle / mean）
curl -s -XPOST localhost:8080/api/v1/plots -H 'Content-Type: application/json' \
  -d '{"code":"P1","name":"合作社A-3号地","sow_date":"2026-04-01",
       "variety":"ZD958","station":"S1","method":"sine"}'

# 3. 批量上报（seq 为上报序号，同站日大序号为准；可乱序、可补传）
curl -s -XPOST localhost:8080/api/v1/observations/batch \
  -H 'Content-Type: application/json' -d '{
    "records":[
      {"station_code":"S1","date":"2026-05-01","tmax":30,"tmin":14,"seq":1},
      {"station_code":"S1","date":"2026-05-02","tmax":9, "tmin":-20,"seq":1}
    ]}'
# -> items 逐条返回 ok/applied/reason；非法记录不影响其余条目

# 4. 更正（更大序号，自动重算并产生事件）
curl -s -XPOST localhost:8080/api/v1/observations/batch \
  -H 'Content-Type: application/json' -d '{
    "records":[{"station_code":"S1","date":"2026-05-01","tmax":24,"tmin":12,"seq":2}]}'

# 5. 改绑（自 6 月 1 日起用 S2）
curl -s -XPOST localhost:8080/api/v1/plots/P1/bindings \
  -H 'Content-Type: application/json' \
  -d '{"station_code":"S2","effective_date":"2026-06-01"}'

# 6. 查询
curl -s 'localhost:8080/api/v1/plots/P1/stages'
curl -s 'localhost:8080/api/v1/plots/P1/daily?from=2026-05-01&to=2026-05-31'
curl -s 'localhost:8080/api/v1/events?after_id=0&limit=100'
```

逐日曲线每行含：`date`、`tmax/tmin`（缺测且无补值时为 null）、
`gdd`（日积温）、`cumulative`（累计）、`source`（observed/filled/climate/
missing）、`fill_method`（neighbor/climate_normal）、`fill_from`。

阶段行含：`stage`、`threshold`、`status`（reached=已达到，给实际日期；
forecast=靠气候平均预计）、`date`、`cumulative_on_date`。

## 历年试走（年际范围、分位、到达把握）

单点预计只给一个“按气候平均外推”的确定日期；历年试走把各站多年逐日
气温各“走”一遍，告诉农户每个未达到阶段大概落在哪段日子、以及某天前
赶到的把握。口径与取舍见 [docs/design.md](docs/design.md) 第 7 节。

按站点和年份导入整年的逐日最高最低气温（一次可带多站多年）；同一站
同一年再导即**整年替换**。一站一年整体生效或整体不生效，任何时候都
查不到导了一半的一年，导入途中重启亦然；同站同年并发导入最终完整
等于其中一次。

```bash
curl -s -XPOST localhost:8080/api/v1/historical/batch \
  -H 'Content-Type: application/json' -d '{
    "records":[
      {"station_code":"S1","date":"2009-06-01","tmax":28,"tmin":18},
      {"station_code":"S1","date":"2009-06-02","tmax":27,"tmin":17}
    ]}'
# items 逐条回 ok/applied/reason；一个站年内有非法记录则整年不生效，
# 合法条 applied=false 并注明被连累；years 给出每个站年是否替换、条数。
```

范围查询（`quantiles` 默认 `0.1,0.5,0.9`）：

```bash
curl -s 'localhost:8080/api/v1/plots/P1/stage-ranges'
```

每个未达到阶段返回：`years_used`（参与年数）与 `years`、`earliest`、
`latest`、`quantiles[]`（每个含 `quantile/date/reachable`——某分位落在
窗口内到不了的年份上时 `reachable=false`，不用最晚日顶替）、
`not_reached_years`（窗口内始终没到的年数，计入年数、不丢弃）。
已达到阶段范围收成实际达到日一天。一块地一个合格历年都没有时
`available=false` 并带原因，不回空结果。

到达比例：

```bash
curl -s 'localhost:8080/api/v1/plots/P1/arrival?stage=tasseling&by=2026-07-18'
# -> fraction=0..1；已达到阶段在实际日前为 0、当天起为 1
```

缺日策略：候选年缺测日由**本站气候平均**顶上，不借邻站；某日历史与
气候平均都缺、或某绑定站段内一个真实历史日都没有，则该年整体不参与。
气候平均会把异常年向平常年拉，范围因此偏窄（保守），详见设计文档
7.2。历年资料与单点预测、逐日曲线、阶段事件完全隔离：导入历年不改变
任何原有结果，录过气候平均的站单点预计仍按气候平均走。

## 校验规则（非法输入返回 400 并带中文原因）

- 最低气温 > 最高气温；温度超出 `[-60, 65] ℃`；
- 基点温度不低于上限温度；五阶段积温需求必须严格递增；
- 上报序号必须为正；同站日相同序号内容不一致报错，小序号晚到不覆盖；
- 播种日期晚于查询日期；改绑到不存在的站、改绑日早于播种日均拒绝；
- 历年记录日期不属于所标年份、同批同站同日重复则该站年整体不生效；
  范围查询分位不在 `[0,1]`、阶段名不存在、到达比例查询日期早于播种日
  均返回 400。

## 测试

```bash
go test ./...
# 随机 200+ 步乱序/更正/补传/改绑序列，逐步比对增量结果与全量重算：
go test ./internal/engine/ -run TestRandomCorrections -v
# 并发（-race）：
go test -race ./internal/engine/
```

PostgreSQL 集成测试（同随机序列在 PG 与内存两后端逐日对账、咨询锁并发、
重启 Recover 对齐）需要数据库，未设置 `DATABASE_URL` 时自动跳过：

```bash
DATABASE_URL='postgres://agri@localhost:5439/agristation?sslmode=disable' \
  go test ./internal/store/ -run TestPG -v
```

测试覆盖：基点 10/最高 30/最低 14 当日积温 12；零与非负；基点调高阶段
日期单调不提前；乱序与重复上报序号仲裁；补值被真实数据自动替换；改绑；
大量随机更正序列下增量与全量重算逐日相等；并发同站日只算一次；重启续跑
结果一致。

历年试走测试覆盖：历史每天都等于气候平均时各分位/最早最晚等于单点
预计日；分位随 q 不减、同一分位跨出苗→成熟不减、到达比例随日期不减；
基点调高分位只推迟；窗口内未到年份计入年数且落在它们身上的分位如实报
“到不了”；已达到阶段收成实际日、不受历年影响；无任何历年年时明确不可用；
随机交错的观测更正、改绑、品种修改、历年导入与整年替换后，范围与比例
逐项等于独立的全量重算参考器；导入事务中途失败回滚看不到半年数据；
同站同年并发导入只留下一份完整内容。PG 集成测试另把历年导入与范围结果
在 PG、内存两后端对账（`TestPGHistoricalRangeParity`）。

## 目录

```
cmd/server/            启动入口
internal/gdd/          日积温口径（平均/单正弦/单三角）
internal/fill/         缺测补值
internal/cum/          累计与阶段推算
internal/ensemble/     历年试走：年份合格判定、逐日积温、分位/比例统计（纯函数）
internal/engine/       增量重算、快照、事件、写入仲裁、历年导入与试走编排
internal/store/        PostgreSQL 实现 + migrations
internal/enginemem/    内存实现与全量重算参考器（含历年试走参考，测试对照）
internal/api/          Gin 路由
internal/model/        数据结构
docs/design.md         口径、补值、快照、历年试走与一致性方案说明
```
