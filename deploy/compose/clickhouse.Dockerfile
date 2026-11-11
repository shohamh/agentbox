FROM clickhouse/clickhouse-server:25.8
COPY configs/control/clickhouse/lean.xml /etc/clickhouse-server/config.d/lean.xml
COPY configs/control/clickhouse/lean-users.xml /etc/clickhouse-server/users.d/lean-users.xml
