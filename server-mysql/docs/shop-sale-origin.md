# Purchase-origin-aware resale

## Settlement

| Purchase | Sale destination | Credit |
| --- | --- | --- |
| Market, paid with YuanBao or Voucher | Market | Voucher balance: floor(paid unit price * count * 85 / 100) |
| Market, paid with YuanBao or Voucher | Ordinary or remote Shop | Coin at the item resale price |
| Coin purchase, MultiShop, consignment, reward, dungeon, crafting, unknown origin | Any supported sale destination | Coin at the item resale price |

Voucher is numeric currency (NumericType 1025), not an inventory attachment.
500 copper = 5 silver = 0.05 gold. Ordinary resale uses the item table Price,
subject to existing Gameplay shop_sale overrides. Missing or nonpositive table
prices use shop_sale.fallback_price_coin (default 500). Currency/experience
goods 110201 through 110206 remain protected from resale.

The recovery percentage is shop_sale.market_recovery_percent (default 85).
Publish config/operations/Gameplay.yaml to MySQL and notify Redis using
cmd/publish-config. Runtime pricing uses the loaded MySQL configuration.
Integer rounding happens on the requested total, so splitting sales can lose
fractional vouchers. A zero-rounded refund is rejected without consuming items.
Currency overflow also rejects the sale without consuming items.

## Protocol boundary

The native C2M_SellItem request contains slot and count, but no destination.
An explicit C2M_GetMarket selects Market for that session; M2C_OpenShopUI
selects ordinary Shop, including the remote-shop route. Automatic market
configuration pushes do not change this selection. Unset selection pays Coin.
All Market pages use the same rule. MultiShop purchases record their own
payment currency and do not become premium Market purchases.

The client and its tooltip assets are unchanged. The server credits the
authoritative amount and pushes SellItem, the full bag, then money updates.
Native GUI drag/drop and tooltip presentation still require client acceptance
testing; handler tests are not a substitute for that visual check.

## Provenance and persistence

Each item batch records purchase source, currency and actual paid unit price.
Only matching origins can stack. Split, sorting, warehouse, direct trading,
equipment persistence and refund-mail delivery retain that origin. Buying a
consignment item replaces its origin with Consignment/StarCoin; an expired,
unsold consignment restores its original origin.

Source IDs: 0 unknown/reward, 1 Shop, 2 Market, 3 MultiShop, 4 Consignment.
Currency IDs: 0 none, 1 Coin, 2 YuanBao, 3 Voucher, 4 StarCoin, 5 Honor,
6 PVP currency, 7 Family contribution.

Normalized child tables (created by the normal schema initialization):

- player_item_purchase_origins: player, location and slot.
- player_mail_item_purchase_origins: player, mail and attachment position.
- consignment_item_purchase_origins: consignment listing ID.

Parent deletion cascades to its child rows. Player saves replace both within
one transaction. Offline consignment refund inserts mail and origin and deletes
the listing in one transaction. Account cloning and SQL export include origins.
Existing items have no reliable historical payment record and remain ordinary
Coin resale; GetSource display text is not used to infer premium eligibility.

## Verification and rollback

- go test ./...
- With MHQ_TEST_MYSQL_DSN set: go test . ./internal/mysqlschema -run 'Test(ItemPurchaseOriginMySQLRoundTrip|SchemaIntegration)' -count=1
- python ./test/verify_sell_twice.py (local temporary account, ordinary resale).

Pre-feature code baseline: baseline-before-sale-origin-20260910 (590635a).
Keep a matching database backup before a production rollout. A binary-only
rollback is not provenance-preserving: old player saves delete parent item
rows and therefore cascade-delete origins. Stop writes before rollback and
restore a matching data snapshot if origin retention is required. Reverting
source alone does not restore balances or sales already performed. New tables
do not need to be dropped to run the old binary.
