-- 链式代理：当前代理需要先经过 chain_proxy_id 指向的前置代理访问网络。
ALTER TABLE proxies ADD COLUMN IF NOT EXISTS chain_proxy_id BIGINT
REFERENCES proxies(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS proxies_chain_proxy_id_idx ON proxies (chain_proxy_id);
