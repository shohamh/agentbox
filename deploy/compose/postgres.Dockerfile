FROM postgres:17
COPY configs/control/postgres/init.sql /docker-entrypoint-initdb.d/10-agentbox.sql
