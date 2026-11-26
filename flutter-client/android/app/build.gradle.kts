plugins {
    id("com.android.application")
    id("dev.flutter.flutter-gradle-plugin")
}
android {
    namespace = "online.sharevps.hqip.preview"
    compileSdk = flutter.compileSdkVersion
    ndkVersion = "30.0.16248370"
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    defaultConfig {
        applicationId = "online.sharevps.hqip.preview"
        minSdk = 26
        targetSdk = flutter.targetSdkVersion
        versionCode = flutter.versionCode
        versionName = flutter.versionName
    }
    signingConfigs {
        create("preview") {
            storeFile = System.getenv("HQIP_PREVIEW_KEYSTORE")?.let { file(it) }
            storePassword = System.getenv("HQIP_PREVIEW_STORE_PASSWORD")
            keyAlias = System.getenv("HQIP_PREVIEW_KEY_ALIAS")
            keyPassword = System.getenv("HQIP_PREVIEW_KEY_PASSWORD")
        }
    }
    buildTypes { release { signingConfig = signingConfigs.getByName("preview") } }
}
kotlin { compilerOptions { jvmTarget = org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17 } }
flutter { source = "../.." }
dependencies {
    implementation(files("libs/hqip-mobile.aar"))
    implementation("androidx.core:core:1.17.0")
}
