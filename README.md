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
| POST | `/observations/batch` | 批量写观测（最多 5000 条/批，逐条回报） |
| GET | `/plots/:code/daily?from=&to=&date=` | 逐日积温曲线 |
| GET | `/plots/:code/stages?date=` | 五阶段日期（reached/forecast） |
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

## 校验规则（非法输入返回 400 并带中文原因）

- 最低气温 > 最高气温；温度超出 `[-60, 65] ℃`；
- 基点温度不低于上限温度；五阶段积温需求必须严格递增；
- 上报序号必须为正；同站日相同序号内容不一致报错，小序号晚到不覆盖；
- 播种日期晚于查询日期；改绑到不存在的站、改绑日早于播种日均拒绝。

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

## 目录

```
cmd/server/            启动入口
internal/gdd/          日积温口径（平均/单正弦/单三角）
internal/fill/         缺测补值
internal/cum/          累计与阶段推算
internal/engine/       增量重算、快照、事件、写入仲裁
internal/store/        PostgreSQL 实现 + migrations
internal/enginemem/    内存实现与全量重算参考器（测试对照）
internal/api/          Gin 路由
internal/model/        数据结构
docs/design.md         口径、补值、快照与一致性方案说明
```
