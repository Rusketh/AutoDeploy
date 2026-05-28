# Branding

> **Status.** Phase 15. A single, system-wide organisational brand —
> configured once, applied everywhere AutoDeploy puts a label on
> something (portal, boot screen, deployed OEM info). Per-image or
> multi-tenant branding is explicitly out of scope (design §12).

## Where it shows up

| Surface              | Field(s) used                                                  |
|----------------------|----------------------------------------------------------------|
| Portal header / login | `product_name`, `logo_data_url`, `primary_color`               |
| Boot Client menu     | `organisation_name`, `logo_data_url`, `product_name`            |
| Deployed Windows OEM | `oem_manufacturer`, `support_url`, `support_phone`, `logo_data_url` (lock-screen logo) |

## Setting the brand

```sh
curl -b cookie.txt -X PUT http://127.0.0.1:8080/api/v1/branding \
    -H 'Content-Type: application/json' \
    -d '{
          "product_name":      "Acme Deploy",
          "organisation_name": "Acme Corporation",
          "support_url":       "https://help.acme.example",
          "support_phone":     "+44 1234 567890",
          "oem_manufacturer":  "Acme IT",
          "primary_color":     "#a3160d",
          "logo_data_url":     "data:image/svg+xml;base64,PHN2Z..."
        }'
```

## Reading the brand

```sh
curl http://127.0.0.1:8080/api/v1/branding
```

GET is open — the portal renders the brand before login, the Boot
Client fetches it to render the menu, and the agent fetches it when
applying OEM information to a freshly imaged machine.

## What "applied to the deployed machine" means

The agent, on first-deploy completion, writes the brand fields to:

```
HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\OEMInformation
  Manufacturer = <oem_manufacturer>
  SupportURL   = <support_url>
  SupportPhone = <support_phone>
  Logo         = <local PNG decoded from logo_data_url, written to C:\Windows\System32\oemlogo.png>
```

These values are what Windows shows in `Settings > System > About > Support`.
