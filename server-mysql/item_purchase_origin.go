package main

const (
	purchaseSourceNone int32 = iota
	purchaseSourceShop
	purchaseSourceMarket
	purchaseSourceMultiShop
	purchaseSourceConsignment
)

const (
	purchaseCurrencyNone int32 = iota
	purchaseCurrencyCoin
	purchaseCurrencyYuanBao
	purchaseCurrencyVoucher
	purchaseCurrencyStarCoin
	purchaseCurrencyHonor
	purchaseCurrencyPVP
	purchaseCurrencyFamilyContribution
)

func samePurchaseOrigin(first, second *bagItem) bool {
	if first == nil || second == nil {
		return false
	}
	return first.PurchaseSource == second.PurchaseSource &&
		first.PurchaseCurrency == second.PurchaseCurrency &&
		first.PurchaseUnitPrice == second.PurchaseUnitPrice
}
