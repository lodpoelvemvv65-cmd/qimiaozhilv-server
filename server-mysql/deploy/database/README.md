# Database import directory

Place the final maintenance-window dump here as `001-mhq.sql` or
`001-mhq.sql.gz`. The MySQL container imports it only when `mysql-data` is
empty. Do not leave an old dump here for a new production deployment.
